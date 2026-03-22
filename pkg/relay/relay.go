// Package relay implements a WebSocket relay for sharing VNC sessions between
// multiple browser viewers. Each viewer gets its own proxied connection to the
// upstream noVNC websockify, so x11vnc's -shared mode serves each independently.
package relay

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	HandshakeTimeout: 10 * time.Second,
	// Allow all origins so users on different machines can connect.
	CheckOrigin: func(r *http.Request) bool { return true },
	// Forward the Sec-WebSocket-Protocol header so noVNC's binary subprotocol
	// is negotiated correctly between the browser and the relay.
	Subprotocols: []string{"binary", "base64"},
}

// Room represents a shareable VNC relay session.
type Room struct {
	Token         string
	ServiceName   string
	// UpstreamWSURL is the websockify WebSocket URL on the noVNC sidecar,
	// e.g. "ws://localhost:34521/websockify"
	UpstreamWSURL string
	CreatedAt     time.Time
}

// Manager holds all active relay rooms keyed by token.
type Manager struct {
	mu    sync.RWMutex
	rooms map[string]*Room
}

// NewManager creates an empty relay Manager.
func NewManager() *Manager {
	return &Manager{rooms: make(map[string]*Room)}
}

// Create registers a new relay room and returns it.
func (m *Manager) Create(serviceName, upstreamWSURL string) *Room {
	room := &Room{
		Token:         NewToken(),
		ServiceName:   serviceName,
		UpstreamWSURL: upstreamWSURL,
		CreatedAt:     time.Now(),
	}
	m.mu.Lock()
	m.rooms[room.Token] = room
	m.mu.Unlock()
	return room
}

// Get returns a room by token, or (nil, false) if not found.
func (m *Manager) Get(token string) (*Room, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.rooms[token]
	return r, ok
}

// Delete removes a room.
func (m *Manager) Delete(token string) {
	m.mu.Lock()
	delete(m.rooms, token)
	m.mu.Unlock()
}

// List returns all active rooms.
func (m *Manager) List() []*Room {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rooms := make([]*Room, 0, len(m.rooms))
	for _, r := range m.rooms {
		rooms = append(rooms, r)
	}
	return rooms
}

// ServeViewer upgrades the incoming HTTP request to a WebSocket connection,
// dials a fresh connection to the upstream noVNC websockify, then relays
// frames bidirectionally until either side disconnects.
//
// Because each viewer gets its own upstream connection, x11vnc's -shared flag
// allows all viewers to see the same desktop simultaneously.
func (r *Room) ServeViewer(w http.ResponseWriter, req *http.Request) error {
	// Negotiate the same subprotocol the browser requested.
	sub := negotiateSubprotocol(req)
	up := upgrader
	up.Subprotocols = []string{sub}

	viewerConn, err := up.Upgrade(w, req, nil)
	if err != nil {
		return fmt.Errorf("relay: viewer upgrade failed: %w", err)
	}
	defer viewerConn.Close()

	// Dial the upstream noVNC websockify.
	header := http.Header{}
	if sub != "" {
		header.Set("Sec-WebSocket-Protocol", sub)
	}
	upstreamConn, _, err := websocket.DefaultDialer.Dial(r.UpstreamWSURL, header)
	if err != nil {
		viewerConn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr,
				"upstream unavailable"))
		return fmt.Errorf("relay: upstream dial failed (%s): %w", r.UpstreamWSURL, err)
	}
	defer upstreamConn.Close()

	errc := make(chan error, 2)

	// upstream → viewer  (screen frames)
	go func() {
		for {
			msgType, data, err := upstreamConn.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			if err := viewerConn.WriteMessage(msgType, data); err != nil {
				errc <- err
				return
			}
		}
	}()

	// viewer → upstream  (keyboard / mouse input)
	go func() {
		for {
			msgType, data, err := viewerConn.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			if err := upstreamConn.WriteMessage(msgType, data); err != nil {
				errc <- err
				return
			}
		}
	}()

	// Block until one side closes.
	<-errc
	return nil
}

// negotiateSubprotocol picks the first recognised subprotocol from the request,
// falling back to "binary" which noVNC always supports.
func negotiateSubprotocol(r *http.Request) string {
	for _, p := range websocket.Subprotocols(r) {
		if p == "binary" || p == "base64" {
			return p
		}
	}
	return "binary"
}
