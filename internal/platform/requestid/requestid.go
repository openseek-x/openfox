package requestid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
)

const Header = "X-Request-ID"

func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get(Header))
		if !valid(id) {
			id = New()
		}
		w.Header().Set(Header, id)
		next.ServeHTTP(w, r.WithContext(WithID(r.Context(), id)))
	})
}

func New() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "request-id-unavailable"
	}
	return hex.EncodeToString(bytes)
}

func valid(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, char := range id {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '-' && char != '_' && char != '.' && char != ':' {
			return false
		}
	}
	return true
}

type contextKey struct{}

func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}

func FromContext(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}
