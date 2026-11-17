package transport

import (
	"context"
	"errors"
	"testing"
)

// A create whose TTL cannot be parsed never reports success: if such a
// request reached a server and came back without PendingExpiresAt, the
// account is deleted and the parse error returned.
func TestEnforcePendingTTL_InvalidTTLUndoesTheCreate(t *testing.T) {
	for _, ttl := range []string{"-1h", "-500ms", "soon"} {
		var deleted []uint64
		del := func(_ context.Context, id uint64) error {
			deleted = append(deleted, id)
			return nil
		}
		u, err := EnforcePendingTTL(context.Background(),
			&CreateUserRequest{Pending: true, PendingTTL: ttl},
			&User{ID: 7, Lifecycle: "pending"}, del)
		var reqErr *RequestError
		if !errors.As(err, &reqErr) || u != nil {
			t.Fatalf("EnforcePendingTTL(%q) = %+v, %v; want a RequestError", ttl, u, err)
		}
		if len(deleted) != 1 || deleted[0] != 7 {
			t.Fatalf("EnforcePendingTTL(%q) deleted %v; want [7]", ttl, deleted)
		}
	}
}
