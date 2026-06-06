package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
)

// GenerateToken creates a 32-byte cryptographically random token.
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("token entropy: %w", err)
	}
	return hex.EncodeToString(b), nil
}
