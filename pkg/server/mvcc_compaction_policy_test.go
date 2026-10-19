// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// policyStateMVCC is an MVCC service with a replicated compaction table: the
// shape a dedicated or shared MVCC shard presents to the namespaced view.
type policyStateMVCC struct {
	service.MVCCKVServicer
	history  bool
	states   map[string]*compaction.State
	readErr  error
	setScope string
	setCtx   context.Context
	set      compaction.Binding
}

var (
	_ service.CompactionStateStore    = (*policyStateMVCC)(nil)
	_ service.RevisionHistoryReporter = (*policyStateMVCC)(nil)
)

func newPolicyStateMVCC(history bool) *policyStateMVCC {
	return &policyStateMVCC{history: history, states: map[string]*compaction.State{}}
}

func (m *policyStateMVCC) KeepsRevisionHistory() bool { return m.history }

func (m *policyStateMVCC) SetCompactionPolicy(ctx context.Context, scope string, b compaction.Binding) error {
	m.setCtx, m.setScope, m.set = ctx, scope, b
	return nil
}

func (m *policyStateMVCC) ClearCompactionState(context.Context, string) error { return nil }

func (m *policyStateMVCC) RecordCompactionSample(context.Context, string, time.Time, int64) error {
	return nil
}

func (m *policyStateMVCC) CompactionState(_ context.Context, scope string) (*compaction.State, error) {
	if m.readErr != nil {
		return nil, m.readErr
	}
	return m.states[scope], nil
}

func (m *policyStateMVCC) CompactionStates(context.Context) (map[string]*compaction.State, error) {
	return m.states, nil
}

// TestNamespacedMVCC_CompactionPolicy_ReadsOnlyItsOwnIncarnation pins the
// view's policy read: the record in its own scope, and only when it was
// bound to this database's owner, name and incarnation.
func TestNamespacedMVCC_CompactionPolicy_ReadsOnlyItsOwnIncarnation(t *testing.T) {
	policy := &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 10}
	binding := compaction.Binding{Tenant: "acme", Database: "db", Incarnation: "i1"}
	cases := map[string]struct {
		state *compaction.State
		want  *compaction.Policy
	}{
		"own incarnation":   {&compaction.State{Policy: policy, Tenant: "acme", Database: "db", Incarnation: "i1"}, policy},
		"older incarnation": {&compaction.State{Policy: policy, Tenant: "acme", Database: "db", Incarnation: "i0"}, nil},
		"other tenant":      {&compaction.State{Policy: policy, Tenant: "other", Database: "db", Incarnation: "i1"}, nil},
		"other database":    {&compaction.State{Policy: policy, Tenant: "acme", Database: "x", Incarnation: "i1"}, nil},
		"no record":         {nil, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			inner := newPolicyStateMVCC(true)
			if tc.state != nil {
				inner.states["acme/db/"] = tc.state
			}
			w := newNamespacedMVCCService(inner, "acme/db/", nil)
			w.binding = binding
			got, err := w.CompactionPolicy(context.Background())
			if err != nil {
				t.Fatalf("CompactionPolicy: %v", err)
			}
			if got != tc.want {
				t.Fatalf("CompactionPolicy = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestNamespacedMVCC_CompactionPolicy_NoneWithoutHistoryOrTable proves a
// store with no compaction table, or one keeping no MVCC history, has no
// policy, and a failed read surfaces its error.
func TestNamespacedMVCC_CompactionPolicy_NoneWithoutHistoryOrTable(t *testing.T) {
	ctx := context.Background()
	if p, err := newNamespacedMVCCService(&globalOnlyMVCC{}, "acme/db/", nil).CompactionPolicy(ctx); p != nil || err != nil {
		t.Fatalf("no table: CompactionPolicy = %v, %v; want nil, nil", p, err)
	}
	inner := newPolicyStateMVCC(false)
	inner.states["acme/db/"] = &compaction.State{Policy: &compaction.Policy{}}
	if p, err := newNamespacedMVCCService(inner, "acme/db/", nil).CompactionPolicy(ctx); p != nil || err != nil {
		t.Fatalf("no history: CompactionPolicy = %v, %v; want nil, nil", p, err)
	}
	boom := errors.New("read failed")
	failing := newPolicyStateMVCC(true)
	failing.readErr = boom
	if _, err := newNamespacedMVCCService(failing, "acme/db/", nil).CompactionPolicy(ctx); !errors.Is(err, boom) {
		t.Fatalf("failed read: err = %v, want %v", err, boom)
	}
}

// TestNamespacedMVCC_SetCompactionPolicy_BindsAndGuards proves the write
// binds the policy to this database's incarnation in its own scope and
// carries the view's drop-fence guard.
func TestNamespacedMVCC_SetCompactionPolicy_BindsAndGuards(t *testing.T) {
	guard, err := service.DropFenceGuard("acme", "db", "")
	if err != nil {
		t.Fatalf("DropFenceGuard: %v", err)
	}
	inner := newPolicyStateMVCC(true)
	w := newNamespacedMVCCService(inner, "acme/db/", nil)
	w.dbGuard = dbGuard{guard: guard}
	w.binding = compaction.Binding{Tenant: "acme", Database: "db", Incarnation: "i1"}
	policy := &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 3}

	if err := w.SetCompactionPolicy(context.Background(), policy); err != nil {
		t.Fatalf("SetCompactionPolicy: %v", err)
	}
	if inner.setScope != "acme/db/" {
		t.Fatalf("scope = %q, want acme/db/", inner.setScope)
	}
	want := compaction.Binding{Policy: policy, Tenant: "acme", Database: "db", Incarnation: "i1"}
	if inner.set != want {
		t.Fatalf("binding = %+v, want %+v", inner.set, want)
	}
	if service.WriteGuardFrom(inner.setCtx).IsZero() {
		t.Fatal("the policy write reached the store without the drop-fence guard")
	}
}

// TestNamespacedMVCC_SetCompactionPolicy_UnavailableWithoutHistory proves
// the write is refused, naming the database, when the store has no table or
// keeps no history.
func TestNamespacedMVCC_SetCompactionPolicy_UnavailableWithoutHistory(t *testing.T) {
	for name, inner := range map[string]service.MVCCKVServicer{
		"no table":   &globalOnlyMVCC{},
		"no history": newPolicyStateMVCC(false),
	} {
		t.Run(name, func(t *testing.T) {
			w := newNamespacedMVCCService(inner, "acme/db/", nil)
			w.binding = compaction.Binding{Database: "db"}
			err := w.SetCompactionPolicy(context.Background(), &compaction.Policy{})
			var unavailable *service.CompactionPolicyUnavailableError
			if !errors.As(err, &unavailable) || unavailable.Database != "db" {
				t.Fatalf("SetCompactionPolicy = %v (%T), want CompactionPolicyUnavailableError for db", err, err)
			}
		})
	}
}

// policyHolderMVCC is an MVCC view that holds its own compaction policy, as
// the namespaced view does beneath the quota wrapper.
type policyHolderMVCC struct {
	service.MVCCKVServicer
	history bool
	policy  *compaction.Policy
	setErr  error
}

var _ service.CompactionPolicyHolder = (*policyHolderMVCC)(nil)

func (m *policyHolderMVCC) KeepsRevisionHistory() bool { return m.history }

func (m *policyHolderMVCC) CompactionPolicy(context.Context) (*compaction.Policy, error) {
	return m.policy, nil
}

func (m *policyHolderMVCC) SetCompactionPolicy(_ context.Context, p *compaction.Policy) error {
	if m.setErr != nil {
		return m.setErr
	}
	m.policy = p
	return nil
}

// TestQuotaMVCC_CompactionPolicy_ForwardsToTheView proves the quota wrapper
// forwards the policy read and write, and the history report, to the view
// it wraps.
func TestQuotaMVCC_CompactionPolicy_ForwardsToTheView(t *testing.T) {
	ctx := context.Background()
	acct, _ := newTestAccountant(t, nil)
	inner := &policyHolderMVCC{history: false}
	q := newQuotaMVCCService(inner, acct, "t1", 100)

	if q.KeepsRevisionHistory() {
		t.Fatal("KeepsRevisionHistory = true, want the view's false")
	}
	policy := &compaction.Policy{Mode: compaction.Revision, RetainRevisions: 2}
	if err := q.SetCompactionPolicy(ctx, policy); err != nil {
		t.Fatalf("SetCompactionPolicy: %v", err)
	}
	if got, err := q.CompactionPolicy(ctx); err != nil || got != policy {
		t.Fatalf("CompactionPolicy = %v, %v; want %v, nil", got, err, policy)
	}
	boom := errors.New("refused")
	inner.setErr = boom
	if err := q.SetCompactionPolicy(ctx, nil); !errors.Is(err, boom) {
		t.Fatalf("SetCompactionPolicy err = %v, want %v", err, boom)
	}
}

// TestQuotaMVCC_CompactionPolicy_ViewWithoutPolicy proves a wrapped view that
// holds no policy reads as none, refuses a write, and is assumed to keep
// history when it does not report.
func TestQuotaMVCC_CompactionPolicy_ViewWithoutPolicy(t *testing.T) {
	ctx := context.Background()
	acct, _ := newTestAccountant(t, nil)
	q := newQuotaMVCCService(&globalOnlyMVCC{}, acct, "t1", 100)

	if !q.KeepsRevisionHistory() {
		t.Fatal("KeepsRevisionHistory = false for a view that does not report")
	}
	if p, err := q.CompactionPolicy(ctx); p != nil || err != nil {
		t.Fatalf("CompactionPolicy = %v, %v; want nil, nil", p, err)
	}
	var unavailable *service.CompactionPolicyUnavailableError
	if err := q.SetCompactionPolicy(ctx, &compaction.Policy{}); !errors.As(err, &unavailable) {
		t.Fatalf("SetCompactionPolicy = %v (%T), want CompactionPolicyUnavailableError", err, err)
	}
}

// TestClusterSealKV_ScanStaleForwards proves the cluster-seal adapter's
// stale scan reads through the system shard's KV service.
func TestClusterSealKV_ScanStaleForwards(t *testing.T) {
	kv := newMockKVService()
	kv.store["seal/a"] = []byte("1")
	kv.store["other/b"] = []byte("2")
	got, err := NewClusterSealKV(kv).ScanStale(context.Background(), "seal/")
	if err != nil {
		t.Fatalf("ScanStale: %v", err)
	}
	if len(got) != 1 || string(got["seal/a"]) != "1" {
		t.Fatalf("ScanStale = %v, want only seal/a", got)
	}
}

// awaitingProbe is a fixed AwaitingAdminCredentialProbe.
type awaitingProbe bool

func (p awaitingProbe) AwaitingAdminCredential() bool { return bool(p) }

// TestServerOptions_SetTheirFields proves each option records its value on
// the server's options.
func TestServerOptions_SetTheirFields(t *testing.T) {
	var o serverOptions
	admitted := false
	admission := TenantMigrationAdmission(func(context.Context) (func(), error) {
		admitted = true
		return func() {}, nil
	})
	for _, opt := range []ServerOption{
		WithDEKKeyDir("/var/lib/qrdb/dek"),
		WithInvitations(nil),
		WithAwaitingAdminCredentialProbe(awaitingProbe(true)),
		WithClusterJoin(nil, nil, "cluster.example.com"),
		WithTenantMigrationAdmission(admission),
	} {
		opt(&o)
	}
	if o.dekKeyDir != "/var/lib/qrdb/dek" {
		t.Fatalf("dekKeyDir = %q", o.dekKeyDir)
	}
	if o.invitations != nil {
		t.Fatalf("invitations = %v, want nil", o.invitations)
	}
	if o.awaitingAdminCredentialProbe == nil || !o.awaitingAdminCredentialProbe.AwaitingAdminCredential() {
		t.Fatal("awaitingAdminCredentialProbe not installed")
	}
	if o.clusterJoinTrustDomain != "cluster.example.com" || o.clusterJoinProvider != nil || o.clusterJoinStore != nil {
		t.Fatalf("cluster join = %v, %v, %q", o.clusterJoinProvider, o.clusterJoinStore, o.clusterJoinTrustDomain)
	}
	if o.tenantMigrationAdmission == nil {
		t.Fatal("tenantMigrationAdmission not installed")
	}
	if _, err := o.tenantMigrationAdmission(context.Background()); err != nil || !admitted {
		t.Fatalf("installed admission was not the one passed: err=%v admitted=%v", err, admitted)
	}
}

// enrollmentBarrier is a sealed barrier reporting a fixed initialization
// state, read under the probe's deadline.
type enrollmentBarrier struct {
	initialized bool
	err         error
	bounded     bool
}

func (b *enrollmentBarrier) IsSealed() bool { return true }

func (b *enrollmentBarrier) Initialized(ctx context.Context) (bool, error) {
	_, b.bounded = ctx.Deadline()
	return b.initialized, b.err
}

// TestSealedWindowListenerProbe_EnrollmentOpenFollowsTheBarrier proves that,
// with no ceremony core, enrollment is open exactly while the barrier reads
// as uninitialized, and a failed read keeps it closed.
func TestSealedWindowListenerProbe_EnrollmentOpenFollowsTheBarrier(t *testing.T) {
	cases := map[string]struct {
		barrier *enrollmentBarrier
		want    bool
	}{
		"uninitialized": {&enrollmentBarrier{}, true},
		"initialized":   {&enrollmentBarrier{initialized: true}, false},
		"read fails":    {&enrollmentBarrier{err: errors.New("store unavailable")}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := sealedWindowListenerProbe{SealedWindowProbe: tc.barrier}
			if got := p.EnrollmentOpen(context.Background()); got != tc.want {
				t.Fatalf("EnrollmentOpen = %v, want %v", got, tc.want)
			}
			if !tc.barrier.bounded {
				t.Fatal("the barrier read ran without a deadline")
			}
		})
	}
}
