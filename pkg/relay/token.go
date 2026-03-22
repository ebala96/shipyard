package relay

import (
	"crypto/rand"
	"encoding/hex"
)

// NewToken generates a cryptographically random 8-byte (16 hex char) token.
func NewToken() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fallback — should never happen.
		return "deadbeef00000000"
	}
	return hex.EncodeToString(b)
}
