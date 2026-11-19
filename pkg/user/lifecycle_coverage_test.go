package user

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// lcfaultKV is the in-memory store with one injected fault per operation,
// limited to keys under a prefix. Everything else behaves as memKVStore.
type lcfaultKV struct {
	*memKVStore
	prefix    string
	getErr    error
	putErr    error
	deleteErr error
	scanErr   error
	// beforeDelete runs once, just before the first Delete under prefix.
	beforeDelete func()
	// beforeGet and afterGet run once, around the first Get under prefix.
	beforeGet func()
	afterGet  func()
}

func (f *lcfaultKV) hit(key string) bool { return strings.HasPrefix(key, f.prefix) }

func (f *lcfaultKV) Get(ctx context.Context, key string) ([]byte, error) {
	if f.getErr != nil && f.hit(key) {
		return nil, f.getErr
	}
	if hook := f.beforeGet; hook != nil && f.hit(key) {
		f.beforeGet = nil
		hook()
	}
	raw, err := f.memKVStore.Get(ctx, key)
	if hook := f.afterGet; hook != nil && f.hit(key) {
		f.afterGet = nil
		hook()
	}
	return raw, err
}

func (f *lcfaultKV) Put(ctx context.Context, key string, value []byte) error {
	if f.putErr != nil && f.hit(key) {
		return f.putErr
	}
	return f.memKVStore.Put(ctx, key, value)
}

func (f *lcfaultKV) Delete(ctx context.Context, key string) error {
	if f.deleteErr != nil && f.hit(key) {
		return f.deleteErr
	}
	if hook := f.beforeDelete; hook != nil && f.hit(key) {
		f.beforeDelete = nil
		hook()
	}
	return f.memKVStore.Delete(ctx, key)
}

func (f *lcfaultKV) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	if f.scanErr != nil && f.hit(prefix) {
		return f.scanErr
	}
	return f.memKVStore.Scan(ctx, prefix, fn)
}

func lcnewFaultStore(t *testing.T) (*lcfaultKV, PendingExpirer, UserServicer) {
	t.Helper()
	kv := &lcfaultKV{memKVStore: newMemKVStore()}
	svc, err := NewStore(kv, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return kv, svc.(PendingExpirer), svc
}

func putMarker(t *testing.T, kv *lcfaultKV, m *reapMarker) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal marker: %v", err)
	}
	if err := kv.memKVStore.Put(context.Background(), reapMarkerKey(m.ID), b); err != nil {
		t.Fatalf("put marker: %v", err)
	}
}

func TestListExpiryClaims_Branches(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("scan unavailable")

	t.Run("scan error", func(t *testing.T) {
		kv, exp, _ := lcnewFaultStore(t)
		kv.prefix, kv.scanErr = keyPrefixReap, boom
		var serr *StoreError
		ids, err := exp.ListExpiryClaims(ctx, "acme")
		if !errors.Is(err, boom) || !errors.As(err, &serr) || ids != nil {
			t.Fatalf("ListExpiryClaims = %v, %v; want a StoreError wrapping the scan error", ids, err)
		}
	})
	t.Run("corrupt marker", func(t *testing.T) {
		kv, exp, _ := lcnewFaultStore(t)
		if err := kv.memKVStore.Put(ctx, reapMarkerKey(9), []byte("{not json")); err != nil {
			t.Fatalf("put: %v", err)
		}
		var serr *StoreError
		if _, err := exp.ListExpiryClaims(ctx, "acme"); !errors.As(err, &serr) || serr.Detail != "unmarshal" {
			t.Fatalf("ListExpiryClaims = %v; want an unmarshal StoreError", err)
		}
	})
	t.Run("system tenant and zero ids", func(t *testing.T) {
		kv, exp, _ := lcnewFaultStore(t)
		putMarker(t, kv, &reapMarker{ID: 7})
		putMarker(t, kv, &reapMarker{ID: 8, TenantID: "acme"})
		// A marker with no id names no account and is never listed.
		b, _ := json.Marshal(&reapMarker{TenantID: "acme"})
		if err := kv.memKVStore.Put(ctx, reapMarkerKey(99), b); err != nil {
			t.Fatalf("put: %v", err)
		}
		ids, err := exp.ListExpiryClaims(ctx, "")
		if err != nil || len(ids) != 1 || ids[0] != 7 {
			t.Fatalf("system ListExpiryClaims = %v, %v; want [7]", ids, err)
		}
		ids, err = exp.ListExpiryClaims(ctx, "acme")
		if err != nil || len(ids) != 1 || ids[0] != 8 {
			t.Fatalf("acme ListExpiryClaims = %v, %v; want [8]", ids, err)
		}
	})
}

func TestClaimExpiredPending_MarkerBranches(t *testing.T) {
	ctx := context.Background()
	later := time.Now().UTC().Add(time.Hour)
	boom := errors.New("kv fault")

	t.Run("marker lookup fails for a gone record", func(t *testing.T) {
		kv, exp, _ := lcnewFaultStore(t)
		kv.prefix, kv.getErr = keyPrefixReap, boom
		if _, err := exp.ClaimExpiredPending(ctx, 4242, later); !errors.Is(err, boom) {
			t.Fatalf("claim = %v, want the marker lookup error", err)
		}
	})
	t.Run("corrupt marker for a gone record", func(t *testing.T) {
		kv, exp, _ := lcnewFaultStore(t)
		if err := kv.memKVStore.Put(ctx, reapMarkerKey(4242), []byte("{")); err != nil {
			t.Fatalf("put: %v", err)
		}
		var serr *StoreError
		if _, err := exp.ClaimExpiredPending(ctx, 4242, later); !errors.As(err, &serr) {
			t.Fatalf("claim = %v, want StoreError", err)
		}
	})
	t.Run("marker answers for a gone record", func(t *testing.T) {
		kv, exp, _ := lcnewFaultStore(t)
		putMarker(t, kv, &reapMarker{ID: 4242, TenantID: "acme", Username: "gone"})
		u, err := exp.ClaimExpiredPending(ctx, 4242, later)
		if err != nil || u == nil || u.ID != 4242 || u.Username != "gone" {
			t.Fatalf("claim = %+v, %v; want the marker's account", u, err)
		}
	})
	t.Run("marker write fails", func(t *testing.T) {
		kv, exp, svc := lcnewFaultStore(t)
		u := createPending(t, svc, "putfail", "acme", time.Minute)
		kv.prefix, kv.putErr = keyPrefixReap, boom
		if _, err := exp.ClaimExpiredPending(ctx, u.ID, later); !errors.Is(err, boom) {
			t.Fatalf("claim = %v, want the marker write error", err)
		}
	})
	t.Run("stale marker read fails", func(t *testing.T) {
		kv, exp, svc := lcnewFaultStore(t)
		u := createPending(t, svc, "fresh", "acme", time.Hour*24)
		kv.prefix, kv.getErr = keyPrefixReap, boom
		if _, err := exp.ClaimExpiredPending(ctx, u.ID, later); !errors.Is(err, boom) {
			t.Fatalf("claim = %v, want the marker read error", err)
		}
	})
	t.Run("already claimed record rewrites its marker", func(t *testing.T) {
		kv, exp, svc := lcnewFaultStore(t)
		u := createPending(t, svc, "twice", "acme", time.Minute)
		if _, err := exp.ClaimExpiredPending(ctx, u.ID, later); err != nil {
			t.Fatalf("first claim: %v", err)
		}
		kv.prefix, kv.putErr = keyPrefixReap, boom
		if _, err := exp.ClaimExpiredPending(ctx, u.ID, later); !errors.Is(err, boom) {
			t.Fatalf("second claim = %v, want the marker write error", err)
		}
		kv.putErr = nil
		again, err := exp.ClaimExpiredPending(ctx, u.ID, later)
		if err != nil || !ExpiryClaimed(again) {
			t.Fatalf("second claim = %+v, %v; want the claimed record", again, err)
		}
	})
}

func TestDeleteExpiredPending_MarkerBranches(t *testing.T) {
	ctx := context.Background()
	later := time.Now().UTC().Add(time.Hour)
	boom := errors.New("kv fault")

	t.Run("no record and no marker", func(t *testing.T) {
		_, exp, _ := lcnewFaultStore(t)
		var notFound *UserNotFoundError
		if _, err := exp.DeleteExpiredPending(ctx, 4242, later); !errors.As(err, &notFound) {
			t.Fatalf("delete = %v, want UserNotFoundError", err)
		}
	})
	t.Run("marker lookup fails", func(t *testing.T) {
		kv, exp, _ := lcnewFaultStore(t)
		kv.prefix, kv.getErr = keyPrefixReap, boom
		if _, err := exp.DeleteExpiredPending(ctx, 4242, later); !errors.Is(err, boom) {
			t.Fatalf("delete = %v, want the marker lookup error", err)
		}
	})
	t.Run("marker finish fails then succeeds", func(t *testing.T) {
		kv, exp, _ := lcnewFaultStore(t)
		putMarker(t, kv, &reapMarker{ID: 4242, TenantID: "acme", Username: "gone"})
		kv.prefix, kv.deleteErr = keyPrefixReap, boom
		if _, err := exp.DeleteExpiredPending(ctx, 4242, later); !errors.Is(err, boom) {
			t.Fatalf("delete = %v, want the marker delete error", err)
		}
		kv.deleteErr = nil
		gone, err := exp.DeleteExpiredPending(ctx, 4242, later)
		if err != nil || gone == nil || gone.ID != 4242 {
			t.Fatalf("delete = %+v, %v; want the marker's account", gone, err)
		}
		if ids, err := exp.ListExpiryClaims(ctx, "acme"); err != nil || len(ids) != 0 {
			t.Fatalf("ListExpiryClaims after the finish = %v, %v; want none", ids, err)
		}
	})
}

// TestRoutingUserService_SealedTenantRefusesLifecycleOps drives every routed
// per-user call against a tenant whose store is sealed: each must surface
// the resolver's refusal rather than fall back to another store.
func TestRoutingUserService_SealedTenantRefusesLifecycleOps(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"acme": newFakeKVStore()},
		sealed:  map[string]bool{},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "acme")
	u, err := svc.CreateUser(ctx, &CreateUserRequest{
		WithoutEmailLoginKey: true, Username: "sealed", TenantID: "acme", Pending: true, PendingTTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	resolver.sealed["acme"] = true
	later := time.Now().UTC().Add(time.Hour)
	exp := svc.(PendingExpirer)
	id := u.ID

	cases := []struct {
		name string
		call func() error
	}{
		{"GetUser", func() error { _, err := svc.GetUser(ctx, id); return err }},
		{"GetUserByEmail", func() error { _, err := svc.GetUserByEmail(ctx, "acme", "sealed@example.com"); return err }},
		{"AddCertBinding", func() error { _, err := svc.AddCertBinding(ctx, id, strings.Repeat("a", 64), 0); return err }},
		{"RemoveCertBinding", func() error { _, err := svc.RemoveCertBinding(ctx, id, strings.Repeat("a", 64)); return err }},
		{"DeleteUser", func() error { return svc.DeleteUser(ctx, id) }},
		{"EnableUser", func() error { return svc.EnableUser(ctx, id) }},
		{"DisableUser", func() error { return svc.DisableUser(ctx, id) }},
		{"ClaimExpiredPending", func() error { _, err := exp.ClaimExpiredPending(ctx, id, later); return err }},
		{"DeleteExpiredPending", func() error { _, err := exp.DeleteExpiredPending(ctx, id, later); return err }},
		{"ListExpiryClaims", func() error { _, err := exp.ListExpiryClaims(ctx, "acme"); return err }},
	}
	if s, ok := svc.(interface {
		SuspendUser(context.Context, uint64) error
		UnsuspendUser(context.Context, uint64) error
	}); ok {
		cases = append(cases,
			struct {
				name string
				call func() error
			}{"SuspendUser", func() error { return s.SuspendUser(ctx, id) }},
			struct {
				name string
				call func() error
			}{"UnsuspendUser", func() error { return s.UnsuspendUser(ctx, id) }},
		)
	}
	if p, ok := svc.(systemPromoter); ok {
		cases = append(cases, struct {
			name string
			call func() error
		}{"PromoteToSystemUser", func() error { _, err := p.PromoteToSystemUser(ctx, id); return err }})
	}
	if a, ok := svc.(interface {
		ActivateWithCertBinding(context.Context, uint64, string, int64) (*User, error)
	}); ok {
		cases = append(cases, struct {
			name string
			call func() error
		}{"ActivateWithCertBinding", func() error {
			_, err := a.ActivateWithCertBinding(ctx, id, strings.Repeat("b", 64), 0)
			return err
		}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); !errors.Is(err, tenant.ErrTenantSealed) {
				t.Fatalf("%s on a sealed tenant = %v, want ErrTenantSealed", tc.name, err)
			}
		})
	}
}

// TestRoutingUserService_UnknownIDRefusesLifecycleOps drives every routed
// lifecycle op with an id the identity index has never seen: the owning
// tenant lookup fails, and each op returns that error before it reaches a
// store.
func TestRoutingUserService_UnknownIDRefusesLifecycleOps(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"acme": newFakeKVStore()},
		sealed:  map[string]bool{},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "acme")
	const id = uint64(0xdeadbeef)
	later := time.Now().UTC().Add(time.Hour)
	exp := svc.(PendingExpirer)

	cases := []struct {
		name string
		call func() error
	}{
		{"GetUser", func() error { _, err := svc.GetUser(ctx, id); return err }},
		{"AddCertBinding", func() error { _, err := svc.AddCertBinding(ctx, id, strings.Repeat("a", 64), 0); return err }},
		{"RemoveCertBinding", func() error { _, err := svc.RemoveCertBinding(ctx, id, strings.Repeat("a", 64)); return err }},
		{"EnableUser", func() error { return svc.EnableUser(ctx, id) }},
		{"DisableUser", func() error { return svc.DisableUser(ctx, id) }},
		{"ClaimExpiredPending", func() error { _, err := exp.ClaimExpiredPending(ctx, id, later); return err }},
		{"DeleteExpiredPending", func() error { _, err := exp.DeleteExpiredPending(ctx, id, later); return err }},
	}
	if s, ok := svc.(interface {
		SuspendUser(context.Context, uint64) error
		UnsuspendUser(context.Context, uint64) error
	}); ok {
		cases = append(cases,
			struct {
				name string
				call func() error
			}{"SuspendUser", func() error { return s.SuspendUser(ctx, id) }},
			struct {
				name string
				call func() error
			}{"UnsuspendUser", func() error { return s.UnsuspendUser(ctx, id) }},
		)
	}
	if p, ok := svc.(systemPromoter); ok {
		cases = append(cases, struct {
			name string
			call func() error
		}{"PromoteToSystemUser", func() error { _, err := p.PromoteToSystemUser(ctx, id); return err }})
	}
	if a, ok := svc.(interface {
		ActivateWithCertBinding(context.Context, uint64, string, int64) (*User, error)
	}); ok {
		cases = append(cases, struct {
			name string
			call func() error
		}{"ActivateWithCertBinding", func() error {
			_, err := a.ActivateWithCertBinding(ctx, id, strings.Repeat("b", 64), 0)
			return err
		}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err == nil {
				t.Fatalf("%s on an unknown id succeeded, want the owning-tenant lookup error", tc.name)
			}
		})
	}
}

// lcplainKV hides the fault store's PutIfAbsent so the store under test
// takes its non-atomic read-then-put path.
type lcplainKV struct{ kvstore.KVStore }

// TestRoutingUserService_ZeroIDRoutesToCaller checks that id 0 reaches the
// caller's own store and gets its ValidationError.
func TestRoutingUserService_ZeroIDRoutesToCaller(t *testing.T) {
	resolver := &fakeResolver{
		shared:  newFakeKVStore(),
		tenants: map[string]kvstore.KVStore{"acme": newFakeKVStore()},
	}
	svc, err := NewRoutingService(resolver, newTestIDGen(t))
	if err != nil {
		t.Fatalf("NewRoutingService: %v", err)
	}
	ctx := tenant.WithTenantID(context.Background(), "acme")
	exp := svc.(PendingExpirer)
	later := time.Now().UTC().Add(time.Hour)
	var invalid *ValidationError
	if _, err := exp.ClaimExpiredPending(ctx, 0, later); !errors.As(err, &invalid) {
		t.Fatalf("claim of id 0 = %v, want ValidationError", err)
	}
	if _, err := exp.DeleteExpiredPending(ctx, 0, later); !errors.As(err, &invalid) {
		t.Fatalf("delete of id 0 = %v, want ValidationError", err)
	}
	if _, err := exp.ListExpiryClaims(ctx, "globex"); err == nil {
		t.Fatal("cross-tenant ListExpiryClaims succeeded, want a refusal")
	}
}

// commonPrefix is the shared leading part of two keys.
func commonPrefix(a, b string) string {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return a[:n]
}

func TestDeleteExpiredPending_MarkerForgetFails(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("delete unavailable")
	kv, exp, _ := lcnewFaultStore(t)
	putMarker(t, kv, &reapMarker{ID: 4242, TenantID: "acme", Username: "gone"})
	kv.prefix, kv.deleteErr = "", boom
	if _, err := exp.DeleteExpiredPending(ctx, 4242, time.Now().UTC().Add(time.Hour)); !errors.Is(err, boom) {
		t.Fatalf("delete = %v, want the store's delete error", err)
	}
}

func TestCreateUser_PrimaryWriteFailureLeavesNoAccount(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("put unavailable")
	kv, _, svc := lcnewFaultStore(t)
	kv.prefix, kv.putErr = commonPrefix(primaryKey(1), primaryKey(2)), boom
	_, err := svc.CreateUser(ctx, &CreateUserRequest{
		WithoutEmailLoginKey: true, Username: "nowrite", TenantID: "acme",
	})
	kv.putErr = nil
	if err == nil {
		return // the store wrote the record in one batch, past the Put fault
	}
	if !errors.Is(err, boom) {
		t.Fatalf("CreateUser = %v, want the primary write error", err)
	}
	if _, err := svc.CreateUser(ctx, &CreateUserRequest{
		WithoutEmailLoginKey: true, Username: "nowrite", TenantID: "acme",
	}); err != nil {
		t.Fatalf("CreateUser after a rolled-back attempt: %v", err)
	}
}

// TestStoreLifecycleOps_ReadFaults drives the store's per-user lifecycle
// calls with a failing read and with an unreadable record: each must
// surface the fault, never act on a record it could not read.
func TestStoreLifecycleOps_ReadFaults(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("read unavailable")
	type op struct {
		name string
		call func(svc UserServicer, id uint64) error
	}
	ops := []op{
		{"GetUser", func(s UserServicer, id uint64) error { _, err := s.GetUser(ctx, id); return err }},
		{"EnableUser", func(s UserServicer, id uint64) error { return s.EnableUser(ctx, id) }},
		{"DisableUser", func(s UserServicer, id uint64) error { return s.DisableUser(ctx, id) }},
		{"DeleteUser", func(s UserServicer, id uint64) error { return s.DeleteUser(ctx, id) }},
		{"GetUserByEmail", func(s UserServicer, _ uint64) error {
			_, err := s.GetUserByEmail(ctx, "acme", "faulty@example.com")
			return err
		}},
		{"SuspendUser", func(s UserServicer, id uint64) error {
			sp, ok := s.(interface {
				SuspendUser(context.Context, uint64) error
			})
			if !ok {
				return boom
			}
			return sp.SuspendUser(ctx, id)
		}},
		{"UnsuspendUser", func(s UserServicer, id uint64) error {
			sp, ok := s.(interface {
				UnsuspendUser(context.Context, uint64) error
			})
			if !ok {
				return boom
			}
			return sp.UnsuspendUser(ctx, id)
		}},
	}
	for _, o := range ops {
		t.Run(o.name+"/read fault", func(t *testing.T) {
			kv, _, svc := lcnewFaultStore(t)
			u := createPending(t, svc, "faulty", "acme", time.Minute)
			kv.prefix, kv.getErr = "", boom
			if err := o.call(svc, u.ID); !errors.Is(err, boom) {
				t.Fatalf("%s with a failing read = %v, want the read error", o.name, err)
			}
		})
		t.Run(o.name+"/unreadable record", func(t *testing.T) {
			kv, _, svc := lcnewFaultStore(t)
			u := createPending(t, svc, "faulty", "acme", time.Minute)
			if err := kv.memKVStore.Put(ctx, primaryKey(u.ID), []byte("{not json")); err != nil {
				t.Fatalf("corrupt record: %v", err)
			}
			if err := o.call(svc, u.ID); err == nil {
				t.Fatalf("%s on an unreadable record succeeded, want an error", o.name)
			}
		})
	}
}

// TestClaimExpiredPending_OverlappingSweepsKeepLaterClaimsMarker runs two
// sweeps with different clocks over one account. The early sweep reads the
// record while it is still within its TTL and drops the marker as stale;
// before that drop lands, the later sweep claims the account. The later
// claim's marker must survive, or the account is stranded: claimed for
// deletion with no marker for the reaper to find.
func TestClaimExpiredPending_OverlappingSweepsKeepLaterClaimsMarker(t *testing.T) {
	ctx := context.Background()
	kv, exp, svc := lcnewFaultStore(t)
	u := createPending(t, svc, "overlap", "acme", time.Hour)
	early := time.Now().UTC()
	late := early.Add(2 * time.Hour)

	var lateClaim *User
	var lateErr error
	kv.prefix = keyPrefixReap
	kv.beforeGet = func() { lateClaim, lateErr = exp.ClaimExpiredPending(ctx, u.ID, late) }

	var notPending *AccountNotPendingError
	if _, err := exp.ClaimExpiredPending(ctx, u.ID, early); !errors.As(err, &notPending) {
		t.Fatalf("early sweep = %v, want AccountNotPendingError", err)
	}
	if lateErr != nil || !ExpiryClaimed(lateClaim) {
		t.Fatalf("late sweep = %+v, %v; want a claimed record", lateClaim, lateErr)
	}
	ids, err := exp.ListExpiryClaims(ctx, "acme")
	if err != nil || len(ids) != 1 || ids[0] != u.ID {
		t.Fatalf("ListExpiryClaims after the overlap = %v, %v; want [%d]", ids, err, u.ID)
	}
	if _, err := exp.DeleteExpiredPending(ctx, u.ID, late); err != nil {
		t.Fatalf("reap of the claimed account: %v", err)
	}
	if ids, err := exp.ListExpiryClaims(ctx, "acme"); err != nil || len(ids) != 0 {
		t.Fatalf("ListExpiryClaims after the reap = %v, %v; want none", ids, err)
	}
}
