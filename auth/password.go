package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"

	"golang.org/x/crypto/pbkdf2"

	"renia/config"
)

// HashPassword returns a base64 salt and derived key for the given password.
func HashPassword(password string) (salt string, hash string, err error) {
	s := make([]byte, config.SaltLength)
	if _, err = io.ReadFull(rand.Reader, s); err != nil {
		return "", "", fmt.Errorf("entropy read: %w", err)
	}
	dk := pbkdf2.Key([]byte(password), s, config.PBKDF2Iterations, config.KeyLength, sha256.New)
	return base64.RawStdEncoding.EncodeToString(s), base64.RawStdEncoding.EncodeToString(dk), nil
}

// VerifyPassword checks password against stored salt and hash using constant-time comparison.
func VerifyPassword(password, saltB64, hashB64 string) bool {
	salt, err1 := base64.RawStdEncoding.DecodeString(saltB64)
	hash, err2 := base64.RawStdEncoding.DecodeString(hashB64)
	if err1 != nil || err2 != nil {
		return false
	}
	dk := pbkdf2.Key([]byte(password), salt, config.PBKDF2Iterations, len(hash), sha256.New)
	return subtle.ConstantTimeCompare(dk, hash) == 1
}
