package seal

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// errDestinationUnopenable is the permanent reopen failure dstFailStores
// returns for every open from the move's destination.
var errDestinationUnopenable = errors.New("destination stores cannot be opened")

// dstFailStores opens like fakeStores but fails every open whose resolved
// root is the destination: a destination that never reopens.
type dstFailStores struct {
	fakeStores
	dst string
}

func (f *dstFailStores) OpenTenantStores(ctx context.Context, tenantID string) error {
	root, err := f.reg.RequireInsideLocalPool(ctx, tenantID)
	if err != nil {
		return err
	}
	if root == f.dst {
		return errDestinationUnopenable
	}
	return f.fakeStores.OpenTenantStores(ctx, tenantID)
}

// settleFailureFixture is moveFixture with the move of moveTestTenant from
// "default" to "fast" admitted for moveTestNode and a real reconciler over
// a real MoveStepRunner whose destination never reopens.
type settleFailureFixture struct {
	fx         *moveFixture
	stores     *dstFailStores
	rec        *volume.MoveReconciler
	before     *volume.Manifest
	fastBefore volume.PoolView
}

func newSettleFailureFixture(t *testing.T) *settleFailureFixture {
	t.Helper()
	ctx := context.Background()
	fx := newMoveFixture(t)
	if err := fx.reg.system.Initialize(ctx, MustCredentials(ctx, []byte("pool-test-pass"))); err != nil {
		t.Fatalf("system barrier Initialize: %v", err)
	}
	stores := &dstFailStores{fakeStores: fakeStores{t: t, reg: fx.reg}, dst: fx.dst()}
	if err := stores.OpenTenantStores(ctx, moveTestTenant); err != nil {
		t.Fatalf("open: %v", err)
	}
	runner, err := NewMoveStepRunner(fx.reg, moveTestNode, stores)
	if err != nil {
		t.Fatalf("NewMoveStepRunner: %v", err)
	}
	report, err := json.Marshal(volume.NodeReport{
		NodeID:     moveTestNode,
		Pools:      map[string]volume.PoolOnNode{"fast": {Mapped: true, Health: volume.Health{State: volume.StateReady}}},
		ReportedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("marshal NodeReport: %v", err)
	}
	if err := fx.kv.Put(ctx, volume.NodeReportKey(moveTestNode), report); err != nil {
		t.Fatalf("Put NodeReport: %v", err)
	}
	fast, err := fx.catalog.GetPool(ctx, "fast")
	if err != nil {
		t.Fatalf("GetPool(fast): %v", err)
	}
	before := mustManifest(t, fx.src)
	if _, err := fx.catalog.AdmitMove(ctx, volume.MoveAdmitRequest{TenantID: moveTestTenant, ToPool: "fast", ReplicaNodes: []string{moveTestNode}}); err != nil {
		t.Fatalf("AdmitMove: %v", err)
	}
	rec, err := volume.NewMoveReconciler(fx.catalog, runner, volume.WithMoveStepAttempts(5))
	if err != nil {
		t.Fatalf("NewMoveReconciler: %v", err)
	}
	return &settleFailureFixture{fx: fx, stores: stores, rec: rec, before: before, fastBefore: fast.PoolView}
}

// pass runs one reconciler pass and this node's steps. Their errors are
// the failures under test; the journal is what the test asserts on.
func (s *settleFailureFixture) pass() {
	ctx := context.Background()
	_ = s.rec.ReconcileOnce(ctx)
	_ = s.rec.RunLocalSteps(ctx, moveTestNode)
}

// move returns the journalled move, or nil once it is gone.
func (s *settleFailureFixture) move(t *testing.T) *volume.Move {
	t.Helper()
	m, err := s.fx.catalog.GetMove(context.Background(), moveTestTenant)
	var notFound *volume.MoveNotFoundError
	if errors.As(err, &notFound) {
		return nil
	}
	if err != nil {
		t.Fatalf("GetMove: %v", err)
	}
	return m
}

// passUntilSettleFails passes until the copy step is recorded and its
// settle has failed once, then checks the journal holds it.
func (s *settleFailureFixture) passUntilSettleFails(t *testing.T) {
	t.Helper()
	for range 20 {
		s.pass()
		if m := s.move(t); m != nil && m.StepSettling && m.StepAttempts > 0 {
			if m.StepNode != moveTestNode || m.State == volume.MoveStateAborting {
				t.Fatalf("move = %+v, want %s's copy step settling", m, moveTestNode)
			}
			// The copy landed: the abort must copy it back, not drop it.
			mustExist(t, filepath.Join(s.fx.dst(), "MANIFEST-000001"))
			if s.stores.open != "" {
				t.Fatalf("stores open = %q after a failed settle, want none", s.stores.open)
			}
			return
		}
	}
	t.Fatalf("the copy step never failed its settle: move = %+v", s.move(t))
}

// passUntilGone passes until the move journal is removed and reports
// whether it went through aborting.
func (s *settleFailureFixture) passUntilGone(t *testing.T) (sawAborting bool) {
	t.Helper()
	for range 20 {
		m := s.move(t)
		if m == nil {
			return sawAborting
		}
		if m.State == volume.MoveStateAborting {
			sawAborting = true
		}
		s.pass()
	}
	t.Fatalf("the move never ended: move = %+v", s.move(t))
	return false
}

// assertRolledBack checks the move left the tenant on its source, intact.
func (s *settleFailureFixture) assertRolledBack(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	fx := s.fx
	if s.stores.open != fx.src {
		t.Fatalf("stores open = %q, want the source", s.stores.open)
	}
	if after := mustManifest(t, fx.src); !reflect.DeepEqual(after, s.before) {
		t.Fatalf("source tree changed by the rollback")
	}
	mustExist(t, filepath.Join(fx.src, "MANIFEST-000001"))
	assertGone(t, fx.dst())
	assertGone(t, filepath.Join(fx.defaultRoot, copyBackStagingName(fx.path)))
	a, err := fx.catalog.Resolve(ctx, moveTestTenant)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if a.Pool != "default" {
		t.Fatalf("assignment pool = %q, want default", a.Pool)
	}
	fast, err := fx.catalog.GetPool(ctx, "fast")
	if err != nil {
		t.Fatalf("GetPool(fast): %v", err)
	}
	if fast.ReservedBytes != s.fastBefore.ReservedBytes || fast.TenantCount != s.fastBefore.TenantCount {
		t.Fatalf("fast reserved/tenants = %d/%d, want %d/%d (pre-admit)",
			fast.ReservedBytes, fast.TenantCount, s.fastBefore.ReservedBytes, s.fastBefore.TenantCount)
	}
}

// TestMoveSettle_PermanentDestinationReopenFailureThenCancel drives the real
// catalog, reconciler and step runner through a copy whose destination
// never reopens, then a cancel: the move aborts with the node copied, the
// copy-back restores the source and the destination is removed.
func TestMoveSettle_PermanentDestinationReopenFailureThenCancel(t *testing.T) {
	s := newSettleFailureFixture(t)
	s.passUntilSettleFails(t)
	if _, err := s.fx.catalog.CancelMove(context.Background(), moveTestTenant); err != nil {
		t.Fatalf("CancelMove: %v", err)
	}
	if !s.passUntilGone(t) {
		t.Fatalf("the cancelled move never went through %s", volume.MoveStateAborting)
	}
	s.assertRolledBack(t)
}

// TestMoveSettle_PermanentDestinationReopenFailureAbortsPastAttempts is the
// same flow with no cancel: the settle's attempt bound aborts the move.
func TestMoveSettle_PermanentDestinationReopenFailureAbortsPastAttempts(t *testing.T) {
	s := newSettleFailureFixture(t)
	s.passUntilSettleFails(t)
	if !s.passUntilGone(t) {
		t.Fatalf("the stalled move never went through %s", volume.MoveStateAborting)
	}
	s.assertRolledBack(t)
}
