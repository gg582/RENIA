package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"golang.org/x/crypto/pbkdf2"
)

const (
	pbkdf2Iters = 600000
	saltLen     = 32
	keyLen      = 32
)

// HashPassword returns a base64 salt and derived key for the given password.
func hashPassword(password string) (salt string, hash string, err error) {
	s := make([]byte, saltLen)
	if _, err = io.ReadFull(rand.Reader, s); err != nil {
		return "", "", fmt.Errorf("entropy read: %w", err)
	}
	dk := pbkdf2.Key([]byte(password), s, pbkdf2Iters, keyLen, sha256.New)
	return base64.RawStdEncoding.EncodeToString(s), base64.RawStdEncoding.EncodeToString(dk), nil
}

// VerifyPassword checks password against stored salt and hash using constant-time comparison.
func verifyPassword(password, saltB64, hashB64 string) bool {
	salt, err1 := base64.RawStdEncoding.DecodeString(saltB64)
	hash, err2 := base64.RawStdEncoding.DecodeString(hashB64)
	if err1 != nil || err2 != nil {
		return false
	}
	dk := pbkdf2.Key([]byte(password), salt, pbkdf2Iters, len(hash), sha256.New)
	return subtle.ConstantTimeCompare(dk, hash) == 1
}

// GenerateToken creates a 32-byte cryptographically random token.
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("token entropy: %w", err)
	}
	return hex.EncodeToString(b), nil
}

type ctxKey int

const userIDKey ctxKey = 0

// UserID extracts the authenticated user identifier from the request context.
func userID(r *http.Request) int64 {
	v, _ := r.Context().Value(userIDKey).(int64)
	return v
}

// WithUserID returns a new context carrying the user identifier.
func withUserID(ctx context.Context, uid int64) context.Context {
	return context.WithValue(ctx, userIDKey, uid)
}

// Register handler.
func (s *Server) Register(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Username == "" || req.Password == "" {
		respondError(w, http.StatusBadRequest, "missing username or password")
		return
	}
	salt, hash, err := hashPassword(req.Password)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "hash failure")
		return
	}
	if _, err = s.db.createUser(r.Context(), req.Username, salt, hash); err != nil {
		respondError(w, http.StatusConflict, "username taken")
		return
	}
	respondJSON(w, http.StatusCreated, createdResponse{Created: true})
}

// Login handler.
func (s *Server) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid json")
		return
	}
	user, err := s.db.getUserByUsername(r.Context(), req.Username)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "lookup error")
		return
	}
	if user == nil || !verifyPassword(req.Password, user.Salt, user.PasswordHash) {
		respondError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	token, err := generateToken()
	if err != nil {
		respondError(w, http.StatusInternalServerError, "token generation")
		return
	}
	if err := s.db.createSession(r.Context(), token, user.ID); err != nil {
		respondError(w, http.StatusInternalServerError, "session creation")
		return
	}
	respondJSON(w, http.StatusOK, LoginResponse{Token: token})
}
