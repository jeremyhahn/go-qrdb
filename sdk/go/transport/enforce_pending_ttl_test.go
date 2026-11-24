package transport

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
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

// TestRequirePendingUserTTL checks the pre-send check of owner ruling 1243:
// a TTL-bearing create reaches only a server that advertises the
// pendingUserTTL capability bit, and a create without a TTL never reads the
// capabilities at all.
func TestRequirePendingUserTTL(t *testing.T) {
	capsErr := errors.New("capabilities unavailable")
	for _, tc := range []struct {
		name      string
		req       *CreateUserRequest
		caps      *CapabilitiesResponse
		capsErr   error
		wantReads int
		check     func(error) bool
	}{
		{name: "nil request", req: nil, check: func(err error) bool { return err == nil }},
		{name: "no TTL", req: &CreateUserRequest{Pending: true}, check: func(err error) bool { return err == nil }},
		{name: "zero TTL", req: &CreateUserRequest{Pending: true, PendingTTL: "0s"}, check: func(err error) bool { return err == nil }},
		{
			name: "bit true", req: &CreateUserRequest{Pending: true, PendingTTL: "24h"},
			caps: &CapabilitiesResponse{PendingUserTTL: true}, wantReads: 1,
			check: func(err error) bool { return err == nil },
		},
		{
			name: "bit false", req: &CreateUserRequest{Pending: true, PendingTTL: "24h"},
			caps: &CapabilitiesResponse{KV: true}, wantReads: 1,
			check: func(err error) bool {
				var cu *CapabilityUnsupportedError
				return errors.As(err, &cu) && strings.Contains(cu.Message, "pendingUserTTL")
			},
		},
		{
			name: "caps error", req: &CreateUserRequest{Pending: true, PendingTTL: "24h"},
			capsErr: capsErr, wantReads: 1,
			check: func(err error) bool {
				var re *RequestError
				return errors.As(err, &re) && re.Method == "CreateUser" && errors.Is(err, capsErr)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			caps := func(context.Context) (*CapabilitiesResponse, error) {
				reads++
				return tc.caps, tc.capsErr
			}
			err := RequirePendingUserTTL(context.Background(), tc.req, caps)
			if !tc.check(err) {
				t.Fatalf("RequirePendingUserTTL = %v", err)
			}
			if reads != tc.wantReads {
				t.Fatalf("capabilities read %d times; want %d", reads, tc.wantReads)
			}
		})
	}
}

// A rollback delete that stalls cannot block the create forever once the
// caller has gone: the detached cleanup context carries its own deadline,
// and the delete's deadline error is joined with the refusal.
func TestEnforcePendingTTL_StalledRollbackIsBounded(t *testing.T) {
	saved := pendingTTLCleanupTimeout
	pendingTTLCleanupTimeout = 20 * time.Millisecond
	t.Cleanup(func() { pendingTTLCleanupTimeout = saved })

	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	del := func(dctx context.Context, _ uint64) error {
		close(entered)
		<-dctx.Done()
		return dctx.Err()
	}
	type result struct {
		u   *User
		err error
	}
	done := make(chan result, 1)
	go func() {
		u, err := EnforcePendingTTL(ctx,
			&CreateUserRequest{Pending: true, PendingTTL: "1h"},
			&User{ID: 7, Lifecycle: "pending"}, del)
		done <- result{u, err}
	}()
	<-entered
	cancel()
	r := <-done
	var capErr *CapabilityUnsupportedError
	if r.u != nil || !errors.As(r.err, &capErr) {
		t.Fatalf("EnforcePendingTTL = %+v, %v; want a CapabilityUnsupportedError", r.u, r.err)
	}
	if !errors.Is(r.err, context.DeadlineExceeded) {
		t.Fatalf("EnforcePendingTTL error %v; want it to carry context.DeadlineExceeded", r.err)
	}
}

// A caller that has already cancelled still gets its unexpiring account
// rolled back: the delete runs on a live context, detached from the caller.
func TestEnforcePendingTTL_CancelledCallerStillRollsBack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var live bool
	var deleted []uint64
	del := func(dctx context.Context, id uint64) error {
		live = dctx.Err() == nil
		deleted = append(deleted, id)
		return nil
	}
	u, err := EnforcePendingTTL(ctx,
		&CreateUserRequest{Pending: true, PendingTTL: "1h"},
		&User{ID: 7, Lifecycle: "pending"}, del)
	var capErr *CapabilityUnsupportedError
	if u != nil || !errors.As(err, &capErr) {
		t.Fatalf("EnforcePendingTTL = %+v, %v; want a CapabilityUnsupportedError", u, err)
	}
	if !live || len(deleted) != 1 || deleted[0] != 7 {
		t.Fatalf("rollback delete live=%v deleted=%v; want a live ctx and [7]", live, deleted)
	}
}
