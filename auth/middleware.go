package auth

import (
	"context"
	"net/http"
)

type ctxKey int

// UserIDKey is the context key for the authenticated user identifier.
const UserIDKey ctxKey = 0

// UserID extracts the authenticated user identifier from the request context.
func UserID(r *http.Request) int64 {
	v, _ := r.Context().Value(UserIDKey).(int64)
	return v
}

// WithUserID returns a new context carrying the user identifier.
func WithUserID(ctx context.Context, uid int64) context.Context {
	return context.WithValue(ctx, UserIDKey, uid)
}
