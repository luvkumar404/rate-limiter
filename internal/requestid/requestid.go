// Package requestid propagates bounded correlation IDs through HTTP and logs.
package requestid

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"
)

type contextKey struct{}

var fallback atomic.Uint64

func Valid(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == ':') {
			return false
		}
	}
	return true
}

func New() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return fmt.Sprintf("%x-%x", time.Now().UnixNano(), fallback.Add(1))
	}
	return hex.EncodeToString(id[:])
}

func WithContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}
func FromContext(ctx context.Context) string { id, _ := ctx.Value(contextKey{}).(string); return id }
