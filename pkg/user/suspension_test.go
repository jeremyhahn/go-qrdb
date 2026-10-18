package user

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// lifecycleOf reads a user's stored lifecycle state.
func lifecycleOf(t *testing.T, svc UserServicer, id uint64) Lifecycle {
	t.Helper()
	u, err := svc.GetUser(context.Background(), id)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	return u.Lifecycle
}

// setLifecycle forces a user into a state through UpdateUser.
func setLifecycle(t *testing.T, svc UserServicer, id uint64, to Lifecycle) {
	t.Helper()
	if _, err := svc.UpdateUser(context.Background(), &UpdateUserRequest{ID: id, Lifecycle: to}); err != nil {
		t.Fatalf("UpdateUser(lifecycle=%s): %v", to, err)
	}
}

func TestSuspendUser_ActiveBecomesSuspended(t *testing.T) {
	svc := newTestStore(t)
	u := createTestUser(t, svc, "alice", "")
	if err := svc.SuspendUser(context.Background(), u.ID); err != nil {
		t.Fatalf("SuspendUser: %v", err)
	}
	if got := lifecycleOf(t, svc, u.ID); got != LifecycleSuspended {
		t.Fatalf("lifecycle: got %q, want suspended", got)
	}
}

func TestSuspendUser_IdempotentOnSuspended(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	u := createTestUser(t, svc, "bob", "")
	if err := svc.SuspendUser(ctx, u.ID); err != nil {
		t.Fatalf("first SuspendUser: %v", err)
	}
	before, err := svc.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if err := svc.SuspendUser(ctx, u.ID); err != nil {
		t.Fatalf("second SuspendUser: %v", err)
	}
	after, err := svc.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if after.Lifecycle != LifecycleSuspended || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("idempotent suspend changed the record: %q at %v, was %v",
			after.Lifecycle, after.UpdatedAt, before.UpdatedAt)
	}
}

func TestUnsuspendUser_SuspendedBecomesActive(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	u := createTestUser(t, svc, "carol", "")
	if err := svc.SuspendUser(ctx, u.ID); err != nil {
		t.Fatalf("SuspendUser: %v", err)
	}
	if err := svc.UnsuspendUser(ctx, u.ID); err != nil {
		t.Fatalf("UnsuspendUser: %v", err)
	}
	if got := lifecycleOf(t, svc, u.ID); got != LifecycleActive {
		t.Fatalf("lifecycle: got %q, want active", got)
	}
}

func TestUnsuspendUser_IdempotentOnActive(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	u := createTestUser(t, svc, "dave", "")
	before, err := svc.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if err := svc.UnsuspendUser(ctx, u.ID); err != nil {
		t.Fatalf("UnsuspendUser: %v", err)
	}
	after, err := svc.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if after.Lifecycle != LifecycleActive || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("idempotent unsuspend changed the record: %q at %v, was %v",
			after.Lifecycle, after.UpdatedAt, before.UpdatedAt)
	}
}

// Every source state other than the verb's own source and target is refused
// with the typed transition error, and the record is left as it was.
func TestSuspension_RefusesOtherSourceStates(t *testing.T) {
	for _, tc := range []struct {
		name string
		from Lifecycle
		verb func(UserServicer, uint64) error
		to   Lifecycle
	}{
		{"suspend disabled", LifecycleDisabled, func(s UserServicer, id uint64) error { return s.SuspendUser(context.Background(), id) }, LifecycleSuspended},
		{"suspend locked", LifecycleLocked, func(s UserServicer, id uint64) error { return s.SuspendUser(context.Background(), id) }, LifecycleSuspended},
		{"suspend pending", LifecyclePending, func(s UserServicer, id uint64) error { return s.SuspendUser(context.Background(), id) }, LifecycleSuspended},
		{"suspend deleting", LifecycleDeleting, func(s UserServicer, id uint64) error { return s.SuspendUser(context.Background(), id) }, LifecycleSuspended},
		{"unsuspend disabled", LifecycleDisabled, func(s UserServicer, id uint64) error { return s.UnsuspendUser(context.Background(), id) }, LifecycleActive},
		{"unsuspend locked", LifecycleLocked, func(s UserServicer, id uint64) error { return s.UnsuspendUser(context.Background(), id) }, LifecycleActive},
		{"unsuspend pending", LifecyclePending, func(s UserServicer, id uint64) error { return s.UnsuspendUser(context.Background(), id) }, LifecycleActive},
		{"unsuspend deleting", LifecycleDeleting, func(s UserServicer, id uint64) error { return s.UnsuspendUser(context.Background(), id) }, LifecycleActive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newTestStore(t)
			u := createTestUser(t, svc, "erin", "")
			if tc.from == LifecyclePending {
				var err error
				u, err = svc.CreateUser(context.Background(), &CreateUserRequest{
					WithoutEmailLoginKey: true, Username: "pending-erin", Pending: true,
				})
				if err != nil {
					t.Fatalf("CreateUser(pending): %v", err)
				}
			} else {
				setLifecycle(t, svc, u.ID, tc.from)
			}
			err := tc.verb(svc, u.ID)
			var te *LifecycleTransitionError
			if !errors.As(err, &te) {
				t.Fatalf("got %v, want *LifecycleTransitionError", err)
			}
			if te.From != tc.from || te.To != tc.to {
				t.Fatalf("transition error %s->%s, want %s->%s", te.From, te.To, tc.from, tc.to)
			}
			if got := lifecycleOf(t, svc, u.ID); got != tc.from {
				t.Fatalf("refused transition changed the record to %q", got)
			}
		})
	}
}

func TestSuspension_ValidationAndNotFound(t *testing.T) {
	svc := newTestStore(t)
	ctx := context.Background()
	for name, verb := range map[string]func(context.Context, uint64) error{
		"suspend":   svc.SuspendUser,
		"unsuspend": svc.UnsuspendUser,
	} {
		var ve *ValidationError
		if err := verb(ctx, 0); !errors.As(err, &ve) {
			t.Errorf("%s(0) = %v, want *ValidationError", name, err)
		}
		var nf *UserNotFoundError
		if err := verb(ctx, 999999); !errors.As(err, &nf) {
			t.Errorf("%s(999999) = %v, want *UserNotFoundError", name, err)
		}
	}
}

func TestSuspension_CommitFailureIsAStoreError(t *testing.T) {
	for _, op := range []string{"suspend", "unsuspend"} {
		t.Run(op, func(t *testing.T) {
			fkv := &faultKVStore{inner: newMemKVStore(), putErr: errors.New("disk full")}
			svc, err := NewStore(fkv, newTestIDGen(t))
			if err != nil {
				t.Fatalf("NewStore: %v", err)
			}
			ctx := context.Background()
			u, err := svc.CreateUser(ctx, &CreateUserRequest{
				WithoutEmailLoginKey: true, Username: "frank", TenantID: "acme",
			})
			if err != nil {
				t.Fatalf("CreateUser: %v", err)
			}
			verb := svc.SuspendUser
			if op == "unsuspend" {
				setLifecycle(t, svc, u.ID, LifecycleSuspended)
				verb = svc.UnsuspendUser
			}
			fkv.putFailAt = fkv.putCount + 1
			var se *StoreError
			if err := verb(ctx, u.ID); !errors.As(err, &se) || se.Op != op {
				t.Fatalf("got %v, want *StoreError with Op %q", err, op)
			}
		})
	}
}

func TestSuspension_NonAtomicStoreIsRefused(t *testing.T) {
	inner := newMemKVStore()
	svc, err := NewStore(inner, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	u := createTestUser(t, svc, "gina", "")
	plain, err := NewStore(nonAtomicKV{inner}, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore(non-atomic): %v", err)
	}
	if err := plain.SuspendUser(context.Background(), u.ID); !errors.Is(err, ErrStoreNotAtomic) {
		t.Fatalf("SuspendUser on a non-atomic store = %v, want ErrStoreNotAtomic", err)
	}
}

// nonAtomicKV hides the inner store's CompareAndSwap.
type nonAtomicKV struct{ kvstore.KVStore }

func TestUserAudit_SuspendUnsuspend(t *testing.T) {
	svc, al := newAuditTestStore(t)
	ctx := auditCtx()

	u := createTestUser(t, svc, "hank", "acme")
	if err := svc.SuspendUser(ctx, u.ID); err != nil {
		t.Fatalf("SuspendUser: %v", err)
	}
	if err := svc.UnsuspendUser(ctx, u.ID); err != nil {
		t.Fatalf("UnsuspendUser: %v", err)
	}
	ev, ok := al.findByAction("suspend")
	if !ok {
		t.Fatal("expected a suspend audit event")
	}
	if ev.Attributes["lifecycle"] != string(LifecycleSuspended) {
		t.Errorf("suspend event lifecycle = %v, want suspended", ev.Attributes["lifecycle"])
	}
	if _, ok := al.findByAction("unsuspend"); !ok {
		t.Error("expected an unsuspend audit event")
	}
}

// A caller in another tenant gets the answer a fabricated id gets, exactly as
// a cross-tenant disable does, and the record is not suspended.
func TestRoutingUserService_ForeignSuspendIsNotFound(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"acme": newFakeKVStore(), "globex": newFakeKVStore()},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "acme")
	created, err := svc.CreateUser(ctx, &CreateUserRequest{WithoutEmailLoginKey: true, Username: "ivan", TenantID: "acme"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	foreign := tenant.WithTenantID(ctx, "globex")
	for name, verb := range map[string]func(context.Context, uint64) error{
		"DisableUser":   svc.DisableUser,
		"SuspendUser":   svc.SuspendUser,
		"UnsuspendUser": svc.UnsuspendUser,
	} {
		var nf *UserNotFoundError
		if err := verb(foreign, created.ID); !errors.As(err, &nf) {
			t.Errorf("foreign %s = %v, want UserNotFoundError", name, err)
		}
	}
	if err := svc.SuspendUser(ctx, created.ID); err != nil {
		t.Fatalf("own-tenant SuspendUser: %v", err)
	}
	read, err := svc.GetUser(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if read.Lifecycle != LifecycleSuspended {
		t.Fatalf("own-tenant lifecycle %q, want suspended", read.Lifecycle)
	}
}
