// Package store provides the etcd-backed state store for Shipyard.
// It replaces the previous in-memory + JSON file persistence.
//
// Key layout:
//   /shipyard/services/{name}          ServiceRecord JSON
//   /shipyard/stacks/{name}/state      StackState JSON (desired + actual)
//   /shipyard/stacks/{name}/ledger/{ts} LedgerEntry JSON (version history)
//   /shipyard/containers/{id}          ContainerRecord JSON
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

const (
	dialTimeout = 5 * time.Second

	// Key prefixes
	prefixServices   = "/shipyard/services/"
	prefixStacks     = "/shipyard/stacks/"
	prefixContainers = "/shipyard/containers/"
	prefixLedger     = "/shipyard/ledger/"
)

// Store is the etcd-backed state store for Shipyard.
type Store struct {
	client    *clientv3.Client
	endpoints []string
}

// New connects to etcd and returns a Store.
// endpoints defaults to ["localhost:2379"] if empty.
func New(endpoints []string) (*Store, error) {
	if len(endpoints) == 0 {
		endpoints = []string{"localhost:2379"}
	}

	cfg := clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: dialTimeout,
	}

	client, err := clientv3.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("store: failed to connect to etcd at %v: %w", endpoints, err)
	}

	// Verify connection with a short ping.
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	if _, err := client.Status(ctx, endpoints[0]); err != nil {
		client.Close()
		return nil, fmt.Errorf("store: etcd not reachable at %v: %w", endpoints, err)
	}

	return &Store{client: client, endpoints: endpoints}, nil
}

// Close closes the etcd connection.
func (s *Store) Close() error {
	return s.client.Close()
}

// Client returns the underlying etcd client (for watch operations).
func (s *Store) Client() *clientv3.Client {
	return s.client
}

// ── Generic helpers ───────────────────────────────────────────────────────

// put serialises v as JSON and writes it to key with an optional TTL.
// ttl=0 means no expiry.
func (s *Store) put(ctx context.Context, key string, v interface{}, ttlSecs int64) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("store: marshal failed for key %q: %w", key, err)
	}

	var opts []clientv3.OpOption
	if ttlSecs > 0 {
		resp, err := s.client.Grant(ctx, ttlSecs)
		if err != nil {
			return fmt.Errorf("store: lease grant failed: %w", err)
		}
		opts = append(opts, clientv3.WithLease(resp.ID))
	}

	_, err = s.client.Put(ctx, key, string(data), opts...)
	if err != nil {
		return fmt.Errorf("store: put failed for key %q: %w", key, err)
	}
	return nil
}

// get fetches a key and deserialises the JSON value into v.
// Returns (false, nil) if the key does not exist.
func (s *Store) get(ctx context.Context, key string, v interface{}) (bool, error) {
	resp, err := s.client.Get(ctx, key)
	if err != nil {
		return false, fmt.Errorf("store: get failed for key %q: %w", key, err)
	}
	if len(resp.Kvs) == 0 {
		return false, nil
	}
	if err := json.Unmarshal(resp.Kvs[0].Value, v); err != nil {
		return false, fmt.Errorf("store: unmarshal failed for key %q: %w", key, err)
	}
	return true, nil
}

// list fetches all keys with the given prefix and deserialises each value.
// fn is called once per matching key with the raw JSON bytes.
func (s *Store) list(ctx context.Context, prefix string, fn func(key string, raw []byte) error) error {
	resp, err := s.client.Get(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return fmt.Errorf("store: list failed for prefix %q: %w", prefix, err)
	}
	for _, kv := range resp.Kvs {
		if err := fn(string(kv.Key), kv.Value); err != nil {
			return err
		}
	}
	return nil
}

// del deletes a key. Silent no-op if key does not exist.
func (s *Store) del(ctx context.Context, key string) error {
	_, err := s.client.Delete(ctx, key)
	if err != nil {
		return fmt.Errorf("store: delete failed for key %q: %w", key, err)
	}
	return nil
}

// delPrefix deletes all keys with the given prefix.
func (s *Store) delPrefix(ctx context.Context, prefix string) error {
	_, err := s.client.Delete(ctx, prefix, clientv3.WithPrefix())
	if err != nil {
		return fmt.Errorf("store: delete prefix failed for %q: %w", prefix, err)
	}
	return nil
}

// Watch returns a channel that fires on any change under the given prefix.
// The caller should range over the returned channel until ctx is cancelled.
func (s *Store) Watch(ctx context.Context, prefix string) clientv3.WatchChan {
	return s.client.Watch(ctx, prefix, clientv3.WithPrefix())
}

// ── Key helpers ───────────────────────────────────────────────────────────

func serviceKey(name string) string {
	return prefixServices + name
}

func stackStateKey(name string) string {
	return prefixStacks + name + "/state"
}

func ledgerKey(name string, ts time.Time) string {
	return fmt.Sprintf("%s%s/ledger/%020d", prefixStacks, name, ts.UnixNano())
}

func containerKey(id string) string {
	return prefixContainers + id
}

// stripPrefix removes the etcd key prefix to get the bare name.
func stripPrefix(key, prefix string) string {
	return strings.TrimPrefix(key, prefix)
}

// ── Raw access helpers (for catalog and other packages) ───────────────────

// RawPut writes a raw string value to a key with no TTL.
func (s *Store) RawPut(ctx context.Context, key, value string) error {
	_, err := s.client.Put(ctx, key, value)
	if err != nil {
		return fmt.Errorf("store: put failed for key %q: %w", key, err)
	}
	return nil
}

// RawGet fetches a key and deserialises the JSON value into v.
func (s *Store) RawGet(ctx context.Context, key string, v interface{}) (bool, error) {
	return s.get(ctx, key, v)
}

// RawList calls fn for each key with the given prefix.
func (s *Store) RawList(ctx context.Context, prefix string, fn func(key string, raw []byte) error) error {
	return s.list(ctx, prefix, fn)
}

// RawDelete deletes a single key.
func (s *Store) RawDelete(ctx context.Context, key string) error {
	return s.del(ctx, key)
}