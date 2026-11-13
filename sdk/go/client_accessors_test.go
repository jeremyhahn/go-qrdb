// Copyright (c) 2025 Jeremy Hahn
// SPDX-License-Identifier: AGPL-3.0-or-later

package sdk_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// newEmbeddedClient returns a client over the embedded transport, which
// implements every capability the accessors probe for, and the transport.
func newEmbeddedClient(t *testing.T, opts ...embedded.Option) (*sdk.Client, *embedded.Transport) {
	t.Helper()
	tr, err := embedded.New(newMockKVService(), &mockAdminService{}, opts...)
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	c, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, tr
}

func TestClient_CapabilityAccessors_ReturnTheTransport(t *testing.T) {
	c, tr := newEmbeddedClient(t)

	accessors := map[string]func() any{
		"Batch":           func() any { return c.Batch() },
		"Lock":            func() any { return c.Lock() },
		"TimeSeries":      func() any { return c.TimeSeries() },
		"Vector":          func() any { return c.Vector() },
		"Vlog":            func() any { return c.Vlog() },
		"Users":           func() any { return c.Users() },
		"Roles":           func() any { return c.Roles() },
		"Certs":           func() any { return c.Certs() },
		"Invitations":     func() any { return c.Invitations() },
		"Database":        func() any { return c.Database() },
		"DEK":             func() any { return c.DEK() },
		"Barrier":         func() any { return c.Barrier() },
		"Ceremony":        func() any { return c.Ceremony() },
		"Credentials":     func() any { return c.Credentials() },
		"ABACPolicies":    func() any { return c.ABACPolicies() },
		"Collections":     func() any { return c.Collections() },
		"SealStore":       func() any { return c.SealStore() },
		"Tenants":         func() any { return c.Tenants() },
		"VolumePools":     func() any { return c.VolumePools() },
		"Backup":          func() any { return c.Backup() },
		"Embedding":       func() any { return c.Embedding() },
		"Rebalance":       func() any { return c.Rebalance() },
		"UserAuthFactors": func() any { return c.UserAuthFactors() },
		"SQL":             func() any { return c.SQL() },
	}
	for name, get := range accessors {
		t.Run(name, func(t *testing.T) {
			if got := get(); got != any(tr) {
				t.Fatalf("%s = %T, want the embedded transport", name, got)
			}
		})
	}
	if !c.SupportsInvitations() {
		t.Fatal("SupportsInvitations = false on the embedded transport")
	}
}

func TestClient_Audit_FollowsTheTransportLogger(t *testing.T) {
	plain, _ := newEmbeddedClient(t)
	if got := plain.Audit(); got != nil {
		t.Fatalf("Audit without a logger = %v, want nil", got)
	}

	logged, _ := newEmbeddedClient(t, embedded.WithAuditLogger(sdk.NewAuditRingBuffer(16)))
	if logged.Audit() == nil {
		t.Fatal("Audit with a logger = nil, want the audit client")
	}
	if err := logged.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := logged.Audit(); got != nil {
		t.Fatalf("Audit after Close = %v, want nil", got)
	}
}

func TestClient_ProviderAccessors_NilAfterClose(t *testing.T) {
	c, _ := newEmbeddedClient(t)
	if c.Permissions() == nil || c.RBAC() == nil || c.Factors() == nil || c.Login() == nil || c.Sessions() == nil {
		t.Fatal("a provider accessor returned nil on an open embedded client")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if c.Permissions() != nil || c.RBAC() != nil || c.Factors() != nil || c.Login() != nil || c.Sessions() != nil {
		t.Fatal("a provider accessor returned a client after Close")
	}
}

func TestClient_WithTenant_PrefixesKeys(t *testing.T) {
	c, _ := newEmbeddedClient(t)
	ctx := context.Background()

	kv, err := c.WithTenant("acme")
	if err != nil {
		t.Fatalf("WithTenant: %v", err)
	}
	if err := kv.Put(ctx, "greeting", []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := c.KV().Get(ctx, "acme/greeting")
	if err != nil {
		t.Fatalf("Get through the root client: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("Get = %q, want %q", got, "hello")
	}
	back, err := kv.Get(ctx, "greeting")
	if err != nil || string(back) != "hello" {
		t.Fatalf("Get through the tenant client = (%q, %v), want hello", back, err)
	}
}

func TestClient_WithTenant_RejectsInvalidID(t *testing.T) {
	c, _ := newEmbeddedClient(t)

	for _, id := range []string{"", "a/b", `a\b`, "a..b"} {
		kv, err := c.WithTenant(id)
		var ce *transport.ConfigError
		if !errors.As(err, &ce) || ce.Field != "tenantID" {
			t.Fatalf("WithTenant(%q) error = %T (%v), want *transport.ConfigError{Field: tenantID}", id, err, err)
		}
		if kv != nil {
			t.Fatalf("WithTenant(%q) client = %#v, want nil alongside the error", id, kv)
		}
	}
}

func TestClusterClient_WatchMembership_ReportsChanges(t *testing.T) {
	ft := &fakeClusterTransport{
		peersResp: peersResponse(1, 1,
			transport.PeerInfo{ReplicaID: 1, Address: "n1:7000", IsLeader: true},
		),
	}
	cc := newFakeClusterClient(t, ft).WithPollInterval(time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := cc.WatchMembership(ctx, 1)
	if err != nil {
		t.Fatalf("WatchMembership: %v", err)
	}
	initial := <-ch
	if initial.LeaderID != 1 || len(initial.Added) != 1 || initial.LeaderChanged {
		t.Fatalf("initial event = %+v, want replica 1 added as leader", initial)
	}

	ft.mu.Lock()
	ft.peersResp = peersResponse(1, 2,
		transport.PeerInfo{ReplicaID: 2, Address: "n2:7000", IsLeader: true},
	)
	ft.mu.Unlock()

	ev := <-ch
	if !ev.LeaderChanged || ev.LeaderID != 2 {
		t.Fatalf("event = %+v, want leadership moved to 2", ev)
	}
	if len(ev.Added) != 1 || ev.Added[0].ReplicaID != 2 {
		t.Fatalf("Added = %+v, want [replica 2]", ev.Added)
	}
	if len(ev.Removed) != 1 || ev.Removed[0].ReplicaID != 1 {
		t.Fatalf("Removed = %+v, want [replica 1]", ev.Removed)
	}

	cancel()
	for range ch {
	}
}

func TestClusterClient_WatchMembership_SkipsReadErrors(t *testing.T) {
	ft := &fakeClusterTransport{
		peersResp: peersResponse(1, 1, transport.PeerInfo{ReplicaID: 1, IsLeader: true}),
	}
	cc := newFakeClusterClient(t, ft).WithPollInterval(time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := cc.WatchMembership(ctx, 1)
	if err != nil {
		t.Fatalf("WatchMembership: %v", err)
	}
	<-ch

	ft.mu.Lock()
	ft.peersErr = errors.New("peers unavailable")
	failingFrom := ft.peersCalls
	ft.mu.Unlock()
	// Wait until the watcher has polled into the error at least twice, then
	// restore the view with a new member: the next event must report it.
	for {
		ft.mu.Lock()
		calls := ft.peersCalls
		ft.mu.Unlock()
		if calls >= failingFrom+2 {
			break
		}
		runtime.Gosched()
	}
	ft.mu.Lock()
	ft.peersErr = nil
	ft.peersResp = peersResponse(1, 1,
		transport.PeerInfo{ReplicaID: 1, IsLeader: true},
		transport.PeerInfo{ReplicaID: 3},
	)
	ft.mu.Unlock()

	ev := <-ch
	if len(ev.Added) != 1 || ev.Added[0].ReplicaID != 3 || ev.LeaderChanged {
		t.Fatalf("event = %+v, want replica 3 added under the same leader", ev)
	}
	cancel()
	for range ch {
	}
}

func TestClusterClient_Leader_PropagatesAdminError(t *testing.T) {
	want := errors.New("shard state unavailable")
	cc := newFakeClusterClient(t, &fakeClusterTransport{shardStateErr: want})

	_, hasLeader, err := cc.Leader(context.Background(), 1)
	if !errors.Is(err, want) || hasLeader {
		t.Fatalf("Leader = (hasLeader %v, %v), want (false, %v)", hasLeader, err, want)
	}
}

func TestDatabaseErrorHelpers(t *testing.T) {
	err := sdk.NewDatabaseError(sdk.ErrConflict, "put", "version moved")
	var dbErr *sdk.DatabaseError
	if !sdk.IsDatabaseError(err, &dbErr) || dbErr.Code != sdk.ErrConflict || dbErr.Op != "put" {
		t.Fatalf("IsDatabaseError = %+v, want code Conflict op put", dbErr)
	}
	if !sdk.IsDatabaseErrorCode(err, sdk.ErrConflict) {
		t.Fatal("IsDatabaseErrorCode(Conflict) = false")
	}
	if sdk.IsDatabaseErrorCode(err, sdk.ErrNotFound) {
		t.Fatal("IsDatabaseErrorCode(NotFound) = true on a conflict")
	}

	var none *sdk.DatabaseError
	if sdk.IsDatabaseError(errors.New("plain"), &none) {
		t.Fatal("IsDatabaseError matched a plain error")
	}
}

func TestRegisterDAODelete_CommitRefusedOnNonAtomicStore(t *testing.T) {
	ctx := context.Background()
	store := sdk.NewMemoryKVStore()
	d, err := sdk.NewDAO(store, "member", func() *reexportMember { return &reexportMember{} })
	if err != nil {
		t.Fatalf("NewDAO: %v", err)
	}
	if err := d.Save(ctx, &reexportMember{ID: 1, Name: "ada", Team: "alpha"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	uow := sdk.NewDAOUnitOfWork(store, sdk.WithDAOUnitOfWorkTimeout(time.Minute))
	if err := sdk.RegisterDAODelete(uow, d, &reexportMember{ID: 1}); err != nil {
		t.Fatalf("RegisterDAODelete: %v", err)
	}
	var refusal *sdk.DAOAtomicBatchUnsupportedError
	if err := uow.Commit(ctx); !errors.As(err, &refusal) {
		t.Fatalf("Commit = %T (%v), want *DAOAtomicBatchUnsupportedError", err, err)
	}
	if _, err := d.Get(ctx, 1); err != nil {
		t.Fatalf("Get after a refused delete: %v, want the record kept", err)
	}
}

func TestAdminFactorWrappers(t *testing.T) {
	enrollable := sdk.EnrollableAdminFactors()
	if len(enrollable) == 0 {
		t.Fatal("EnrollableAdminFactors is empty")
	}
	for _, f := range enrollable {
		if !sdk.IsEnrollableAdminFactor(f) {
			t.Fatalf("IsEnrollableAdminFactor(%q) = false for a listed factor", f)
		}
	}
	if sdk.IsEnrollableAdminFactor(sdk.AdminFactor("bogus")) {
		t.Fatal("IsEnrollableAdminFactor(bogus) = true")
	}
	if names := sdk.AdminFactorNames(); !slices.Contains(names, string(sdk.FactorFIDO2)) {
		t.Fatalf("AdminFactorNames = %v, want it to name fido2", names)
	}

	if _, err := sdk.ParseAdminFactors([]string{"bogus"}); err == nil {
		t.Fatal("ParseAdminFactors(bogus) succeeded")
	} else {
		var ufe *sdk.UnsupportedAdminFactorError
		if !errors.As(err, &ufe) {
			t.Fatalf("ParseAdminFactors(bogus) = %T (%v), want *UnsupportedAdminFactorError", err, err)
		}
	}

	plan, err := sdk.NewAdminEnrollmentPlan(nil)
	if err != nil {
		t.Fatalf("NewAdminEnrollmentPlan: %v", err)
	}
	if !plan.Includes(sdk.FactorMTLS) {
		t.Fatalf("empty plan factors = %v, want mTLS", plan.Factors())
	}

	_, err = sdk.EnrollAdminFactors(context.Background(), plan, nil, "admin", "acme",
		nil, nil)
	if !errors.Is(err, sdk.ErrNilAdminFactorRegistry) {
		t.Fatalf("EnrollAdminFactors(nil registry) = %v, want ErrNilAdminFactorRegistry", err)
	}
}

func TestEnrollmentSpecWrappers(t *testing.T) {
	spec, err := sdk.ParseEnrollmentSpec("alice:csrs/alice.pem")
	if err != nil {
		t.Fatalf("ParseEnrollmentSpec: %v", err)
	}
	if spec.Username != "alice" || spec.Kind != sdk.EnrollmentKindCSR || spec.Path != "csrs/alice.pem" {
		t.Fatalf("spec = %+v, want alice csr csrs/alice.pem", spec)
	}

	if _, err := sdk.ParseEnrollmentSpec(""); err == nil {
		t.Fatal("ParseEnrollmentSpec(empty) succeeded")
	} else {
		var ise *ceremony.InvalidEnrollmentSpecError
		if !errors.As(err, &ise) {
			t.Fatalf("ParseEnrollmentSpec(empty) = %T, want *InvalidEnrollmentSpecError", err)
		}
	}

	specs, err := sdk.ParseEnrollmentSpecs([]string{"alice:cert:a.pem", "bob:pubkey:b.pub"})
	if err != nil {
		t.Fatalf("ParseEnrollmentSpecs: %v", err)
	}
	if len(specs) != 2 || specs[0].Kind != sdk.EnrollmentKindCert || specs[1].Kind != sdk.EnrollmentKindPubKey {
		t.Fatalf("specs = %+v, want cert then pubkey", specs)
	}

	_, err = sdk.ParseEnrollmentSpecs([]string{"alice:a.csr", "alice:b.csr"})
	var dup *ceremony.DuplicateEnrollmentUsernameError
	if !errors.As(err, &dup) {
		t.Fatalf("ParseEnrollmentSpecs(duplicate) = %T (%v), want *DuplicateEnrollmentUsernameError", err, err)
	}

	if kinds := sdk.EnrollmentKindNames(); !slices.Contains(kinds, string(sdk.EnrollmentKindCSR)) {
		t.Fatalf("EnrollmentKindNames = %v, want it to name csr", kinds)
	}
}

func TestInviteWrappers(t *testing.T) {
	if _, err := sdk.NewTenantInviteStore(nil); !errors.Is(err, sdk.ErrInviteNilStoreResolver) {
		t.Fatalf("NewTenantInviteStore(nil) = %v, want ErrInviteNilStoreResolver", err)
	}

	a, err := sdk.GenerateInviteToken()
	if err != nil {
		t.Fatalf("GenerateInviteToken: %v", err)
	}
	b, err := sdk.GenerateInviteToken()
	if err != nil {
		t.Fatalf("GenerateInviteToken: %v", err)
	}
	if a == "" || a == b {
		t.Fatalf("tokens %q and %q, want two distinct non-empty tokens", a, b)
	}

	sum := sha256.Sum256([]byte(a))
	if got, want := sdk.InviteTokenDigest(a), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("InviteTokenDigest = %q, want %q", got, want)
	}
}
