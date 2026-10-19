// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// cmMemKV is an in-memory clustermig.KV for the daemon adapter tests.
type cmMemKV struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newCMMemKV() *cmMemKV { return &cmMemKV{m: map[string][]byte{}} }

func (k *cmMemKV) Put(_ context.Context, key string, v []byte) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[key] = append([]byte(nil), v...)
	return nil
}
func (k *cmMemKV) Get(_ context.Context, key string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.m[key], nil
}
func (k *cmMemKV) PutAllIfUnchanged(_ context.Context, guardKey string, guardValue []byte, writes []kvstore.KV) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	cur, ok := k.m[guardKey]
	if guardValue == nil && ok || guardValue != nil && (!ok || !bytes.Equal(cur, guardValue)) {
		return false, nil
	}
	for _, w := range writes {
		if w.Delete {
			delete(k.m, w.Key)
			continue
		}
		k.m[w.Key] = bytes.Clone(w.Value)
	}
	return true, nil
}
func (k *cmMemKV) Scan(_ context.Context, prefix string, fn func(string, []byte) error) error {
	k.mu.Lock()
	snap := map[string][]byte{}
	for key, v := range k.m {
		if strings.HasPrefix(key, prefix) {
			snap[key] = v
		}
	}
	k.mu.Unlock()
	for key, v := range snap {
		if err := fn(key, v); err != nil {
			return err
		}
	}
	return nil
}

// recordingMigrator records the scope + credential + full options it received.
type recordingMigrator struct {
	scope service.BarrierScope
	cred  []byte
	opts  seal.MigrationOptions
	err   error
}

func (m *recordingMigrator) EnableEncryption(_ context.Context, scope service.BarrierScope, opts seal.MigrationOptions) error {
	m.scope = scope
	m.cred = sealtest.Snapshot(opts.Credentials)
	m.opts = opts
	return m.err
}

// TestDaemonLocalRecycler_RoutesAndCopies proves the recycler maps the record to
// the right BarrierScope, hands the migrator its OWN copy of the credential, and
// guards the nil-migrator case.
func TestDaemonLocalRecycler_RoutesAndCopies(t *testing.T) {
	ctx := context.Background()

	// System scope.
	mig := &recordingMigrator{}
	r := &daemonLocalRecycler{migrator: mig}
	cred := []byte("sys-secret")
	if err := r.Recycle(ctx, &clustermig.ClusterMigration{Target: seal.StrategyPassphrase}, cred); err != nil {
		t.Fatalf("Recycle(system): %v", err)
	}
	if mig.scope.Kind != service.ScopeSystem {
		t.Fatalf("system scope = %q", mig.scope.Kind)
	}
	if string(mig.cred) != "sys-secret" {
		t.Fatalf("migrator cred = %q", mig.cred)
	}
	// The recycler must seal its OWN copy: the caller's buffer is left intact
	// for the caller to wipe, and wiping it must not affect what the migrator
	// was given.
	if string(cred) != "sys-secret" {
		t.Fatalf("recycler wiped the caller's buffer: %q", cred)
	}
	for i := range cred {
		cred[i] = 0
	}
	if string(mig.cred) != "sys-secret" {
		t.Fatal("recycler did not copy the credential (use-after-wipe risk)")
	}

	// Tenant scope.
	mig2 := &recordingMigrator{}
	r2 := &daemonLocalRecycler{migrator: mig2}
	if err := r2.Recycle(ctx, &clustermig.ClusterMigration{TenantID: "acme", Target: seal.StrategyPassphrase}, []byte("x")); err != nil {
		t.Fatalf("Recycle(tenant): %v", err)
	}
	if mig2.scope.Kind != service.ScopeTenant || mig2.scope.TenantID != "acme" {
		t.Fatalf("tenant scope = %+v", mig2.scope)
	}

	// Nil migrator → unavailable.
	rNil := &daemonLocalRecycler{}
	if err := rNil.Recycle(ctx, &clustermig.ClusterMigration{Target: seal.StrategyPassphrase}, nil); !errors.Is(err, service.ErrMigrationProviderUnavailable) {
		t.Fatalf("Recycle(nil migrator) = %v, want ErrMigrationProviderUnavailable", err)
	}
}

// TestDaemonLocalRecycler_CarriesShamirFields proves Recycle threads the
// record's ShamirThreshold/ShamirTotal into the seal.MigrationOptions it hands
// the local migrator. This is the exact field that was missing: without it,
// every reconciler tick reconstructed threshold=0/total=0 regardless of what
// the operator originally requested, and seal.MigrationOptions.Validate()
// rejected the Shamir target on every retry, forever.
func TestDaemonLocalRecycler_CarriesShamirFields(t *testing.T) {
	ctx := context.Background()
	mig := &recordingMigrator{}
	r := &daemonLocalRecycler{migrator: mig}

	m := &clustermig.ClusterMigration{
		Target:          seal.StrategyShamir,
		Mechanism:       seal.MechanismSwap,
		ShamirThreshold: 2,
		ShamirTotal:     3,
	}
	if err := r.Recycle(ctx, m, nil); err != nil {
		t.Fatalf("Recycle: %v", err)
	}
	if mig.opts.TargetStrategy != seal.StrategyShamir {
		t.Fatalf("opts.TargetStrategy = %q, want shamir", mig.opts.TargetStrategy)
	}
	if mig.opts.ShamirThreshold != 2 || mig.opts.ShamirTotal != 3 {
		t.Fatalf("opts shamir fields = %d/%d, want 2/3", mig.opts.ShamirThreshold, mig.opts.ShamirTotal)
	}
	// The reconstructed options must themselves pass Validate (the bug's
	// symptom was ErrShamirThresholdInvalid on every single retry).
	if verr := mig.opts.Validate(); verr != nil {
		t.Fatalf("reconstructed opts failed Validate: %v", verr)
	}
}

// TestDaemonLocalRecycler_AtTarget proves the idempotency check the reconciler
// asks before it takes a credential: only a local barrier that already reports
// the target strategy is at target, and an unavailable barrier fails open — the
// post-reboot/re-issue resume path.
func TestDaemonLocalRecycler_AtTarget(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.DiscardHandler)

	// A keyed (software/passphrase) system barrier registry.
	strategy, err := seal.NewSoftwareStrategy(context.Background(), []byte("pw"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	sb, err := seal.NewStorageBarrier(logger, seal.NewMemoryBackend(), seal.BarrierConfig{RootKeyPath: "system/root-key"}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := sb.Initialize(ctx, seal.MustCredentials(context.Background(), []byte("pw"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	reg, err := seal.NewBarrierRegistry(logger, sb)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	r := &daemonLocalRecycler{migrator: &recordingMigrator{}, barrierReg: reg}
	// Target == the barrier's current strategy → at target.
	if !r.AtTarget(&clustermig.ClusterMigration{Target: sb.Status().Strategy}) {
		t.Fatal("system barrier at the target strategy must report AtTarget")
	}
	// A DIFFERENT target → not at target.
	if r.AtTarget(&clustermig.ClusterMigration{Target: seal.StrategyShamir}) {
		t.Fatal("a different target strategy must not report AtTarget")
	}
	// A tenant scope with no tenant barrier → not at target (fails open).
	if r.AtTarget(&clustermig.ClusterMigration{TenantID: "absent", Database: "db", Target: sb.Status().Strategy}) {
		t.Fatal("a missing tenant barrier must not report AtTarget")
	}
	// No registry → not at target (fails open).
	if (&daemonLocalRecycler{}).AtTarget(&clustermig.ClusterMigration{Target: sb.Status().Strategy}) {
		t.Fatal("a recycler without a registry must not report AtTarget")
	}
}

// fixedResolver satisfies the clustermig owner seam for the coordinator
// migrator test.
type fixedResolver struct {
	shard  uint64
	owners []string
}

func (f fixedResolver) Owners(context.Context, string, string) (uint64, []string, error) {
	return f.shard, f.owners, nil
}

// TestClusterCoordinatorMigrator proves the transport migrator writes a
// replicated intent for system and tenant scope, and that the record carries
// no credential: the node that accepted the request holds the request's
// credential in memory for its own recycle, and only when it owns the scope.
func TestClusterCoordinatorMigrator(t *testing.T) {
	ctx := context.Background()
	store := clustermig.NewStore(newCMMemKV())
	coord := clustermig.NewCoordinator(store, fixedResolver{shard: 1, owners: []string{"n1"}},
		func() bool { return true }, func() int64 { return 1 })
	local := clustermig.NewLocalCredentials("")
	m := &clusterCoordinatorMigrator{coord: coord, credentials: local, self: "n1"}

	if err := m.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, validMigOpts()); err != nil {
		t.Fatalf("EnableEncryption(system): %v", err)
	}
	sys, _ := store.GetMigration(ctx, "", "")
	if sys == nil {
		t.Fatal("system migration intent was not written")
	}
	if got, err := local.Credential(ctx, sys); err != nil || string(got) != "pw" {
		t.Fatalf("held credential = %q, %v; want the request's", got, err)
	}
	if err := m.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeTenant, TenantID: "acme"}, validMigOpts()); err != nil {
		t.Fatalf("EnableEncryption(tenant): %v", err)
	}
	if got, _ := store.GetMigration(ctx, "acme", ""); got == nil {
		t.Fatal("tenant migration intent was not written")
	}
}

// TestClusterCoordinatorMigrator_HoldsOnlyWhatItNeeds proves the accepting
// node holds nothing for a scope it does not own, nor for a Shamir target,
// nor for a request that carries no credential to hold.
func TestClusterCoordinatorMigrator_HoldsOnlyWhatItNeeds(t *testing.T) {
	ctx := context.Background()
	cases := map[string]struct {
		owners []string
		opts   seal.MigrationOptions
	}{
		"not an owner": {[]string{"n2"}, validMigOpts()},
		"no credential": {[]string{"n1"}, func() seal.MigrationOptions {
			o := validMigOpts()
			o.Credentials = seal.Credentials{}
			return o
		}()},
		"shamir target": {[]string{"n1"}, seal.MigrationOptions{
			TargetStrategy: seal.StrategyShamir, Mechanism: seal.MechanismSwap,
			Availability: seal.AvailabilityQuiesced, ShamirThreshold: 2, ShamirTotal: 3,
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := clustermig.NewStore(newCMMemKV())
			coord := clustermig.NewCoordinator(store, fixedResolver{shard: 1, owners: tc.owners},
				func() bool { return true }, nil)
			local := clustermig.NewLocalCredentials("")
			m := &clusterCoordinatorMigrator{coord: coord, credentials: local, self: "n1"}
			if err := m.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, tc.opts); err != nil {
				t.Fatal(err)
			}
			rec, _ := store.GetMigration(ctx, "", "")
			if rec == nil {
				t.Fatal("intent not written")
			}
			if _, err := local.Credential(ctx, rec); !errors.Is(err, clustermig.ErrCredentialAwaited) {
				t.Fatalf("held a credential: err = %v", err)
			}
		})
	}
}

// errModeDenied is the entitlement refusal modeCheckFunc returns.
var errModeDenied = errors.New("barrier mode not entitled")

// modeCheckFunc adapts a function to the barrierModeChecker seam.
type modeCheckFunc func(ctx context.Context, tenantID string, mode seal.StrategyID) error

func (f modeCheckFunc) CheckBarrierMode(ctx context.Context, tenantID string, mode seal.StrategyID) error {
	return f(ctx, tenantID, mode)
}

// TestClusterCoordinatorMigrator_TenantGates proves a tenant-scope request is
// refused at request time, before any intent is written, when the tenant has
// no dedicated barrier, names no tenant, or is not entitled to the target.
func TestClusterCoordinatorMigrator_TenantGates(t *testing.T) {
	ctx := context.Background()
	reg, err := seal.NewBarrierRegistry(slog.New(slog.DiscardHandler), newRecodeSourceBarrier(t))
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	cases := map[string]struct {
		tenantID  string
		reg       *seal.BarrierRegistry
		modeCheck barrierModeChecker
		want      error
	}{
		"shared barrier":  {tenantID: "acme", reg: reg, want: service.ErrSharedBarrierImmutable},
		"empty tenant id": {tenantID: "", reg: reg, want: seal.ErrEmptyTenantID},
		"not entitled": {
			tenantID: "acme",
			modeCheck: modeCheckFunc(func(_ context.Context, tenantID string, mode seal.StrategyID) error {
				if tenantID != "acme" || mode != seal.StrategyPassphrase {
					t.Errorf("CheckBarrierMode(%q, %q), want (acme, passphrase)", tenantID, mode)
				}
				return errModeDenied
			}),
			want: errModeDenied,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := clustermig.NewStore(newCMMemKV())
			coord := clustermig.NewCoordinator(store, fixedResolver{shard: 1, owners: []string{"n1"}},
				func() bool { return true }, func() int64 { return 1 })
			m := &clusterCoordinatorMigrator{
				coord: coord, credentials: clustermig.NewLocalCredentials(""), self: "n1",
				barrierReg: tc.reg, modeCheck: tc.modeCheck,
			}
			err := m.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeTenant, TenantID: tc.tenantID}, validMigOpts())
			if !errors.Is(err, tc.want) {
				t.Fatalf("EnableEncryption = %v, want %v", err, tc.want)
			}
			if rec, _ := store.GetMigration(ctx, tc.tenantID, ""); rec != nil {
				t.Fatal("a refused request wrote a migration intent")
			}
		})
	}
}

// TestCoordinateMigrations_ReapsCompleted proves the leader reaps a completed migration and
// leaves an incomplete one in place.
func TestCoordinateMigrations_ReapsCompleted(t *testing.T) {
	ctx := context.Background()
	store := clustermig.NewStore(newCMMemKV())
	coord := clustermig.NewCoordinator(store, fixedResolver{shard: 1, owners: []string{"n1"}},
		func() bool { return true }, func() int64 { return 1 })

	// Incomplete migration: not reaped.
	if _, err := coord.Request(ctx, "acme", "", validMigOpts()); err != nil {
		t.Fatalf("Request: %v", err)
	}
	coordinateMigrations(ctx, coord, store)
	if got, _ := store.GetMigration(ctx, "acme", ""); got == nil {
		t.Fatal("incomplete migration must not be reaped")
	}
	// Complete it → reaped.
	if err := store.PutNodeStatus(ctx, "acme", "", &clustermig.NodeMigrationStatus{NodeID: "n1", Generation: 1, Phase: seal.PhaseComplete}); err != nil {
		t.Fatalf("PutNodeStatus: %v", err)
	}
	coordinateMigrations(ctx, coord, store)
	// GetMigration returns (nil, nil) once the record is reaped and (nil, err)
	// if the read itself failed; checking only got == nil scores a broken read
	// as "reaped", the same defect #236 fixed elsewhere and #255 found #236 had
	// missed for this store's absent-is-(nil,nil) contract.
	got, err := store.GetMigration(ctx, "acme", "")
	if err != nil {
		t.Fatalf("GetMigration after reap: %v -- the store read failed, so reap was never confirmed", err)
	}
	if got != nil {
		t.Fatal("completed migration must be reaped")
	}
}

func TestCoordinateMigrations_AdvancesARollingMigration(t *testing.T) {
	ctx := context.Background()
	store := clustermig.NewStore(newCMMemKV())
	coord := clustermig.NewCoordinator(store, fixedResolver{shard: 1, owners: []string{"n1", "n2"}},
		func() bool { return true }, func() int64 { return 1 },
		clustermig.WithRollingSteps(func() bool { return true }, "n1"))
	if _, err := coord.Request(ctx, "acme", "", validMigOpts()); err != nil {
		t.Fatalf("Request: %v", err)
	}
	complete := func(node string) {
		t.Helper()
		if err := store.PutNodeStatus(ctx, "acme", "", &clustermig.NodeMigrationStatus{NodeID: node, Generation: 1, Phase: seal.PhaseComplete}); err != nil {
			t.Fatalf("PutNodeStatus: %v", err)
		}
	}
	// n2 recycles first; once it acks, a tick hands the turn to n1.
	complete("n2")
	coordinateMigrations(ctx, coord, store)
	got, err := store.GetMigration(ctx, "acme", "")
	if err != nil || got == nil {
		t.Fatalf("GetMigration = %v, %v; want the rolling record", got, err)
	}
	want := []clustermig.NodeStep{{NodeID: "n2", State: clustermig.StepServing}, {NodeID: "n1", State: clustermig.StepRecycling}}
	if len(got.Steps) != len(want) || got.Steps[0] != want[0] || got.Steps[1] != want[1] {
		t.Fatalf("Steps = %+v, want %+v", got.Steps, want)
	}
	// n1's ack: the same tick records it serving and reaps the record.
	complete("n1")
	coordinateMigrations(ctx, coord, store)
	if got, err := store.GetMigration(ctx, "acme", ""); err != nil || got != nil {
		t.Fatalf("GetMigration after the last step = %+v, %v; want reaped", got, err)
	}
}

// shardProgress is a clustermig.ReplicationProgress for one shard whose
// leadership the test sets: every replica holds the log up to commit.
type shardProgress struct {
	leads  bool
	commit uint64
}

var _ clustermig.ReplicationProgress = shardProgress{}

func (p shardProgress) Leads(uint64) bool { return p.leads }

func (p shardProgress) CommitIndex(uint64) (uint64, bool, error) { return p.commit, true, nil }

func (p shardProgress) MatchIndex(uint64, string) (uint64, bool, error) { return p.commit, true, nil }

// TestCoordinateMigrations_TheShardLeaderAdvances proves a tick on a node that
// follows the system shard still advances a rolling migration on a shard it
// leads, and a tick on the system shard's leader, not leading that shard,
// leaves it alone.
func TestCoordinateMigrations_TheShardLeaderAdvances(t *testing.T) {
	ctx := context.Background()
	store := clustermig.NewStore(newCMMemKV())
	coordinator := func(systemLeader, shardLeader bool) *clustermig.Coordinator {
		return clustermig.NewCoordinator(store, fixedResolver{shard: 9, owners: []string{"n1", "n2"}},
			func() bool { return systemLeader }, func() int64 { return 1 },
			clustermig.WithRollingSteps(func() bool { return true }, "n1"),
			clustermig.WithCatchUp(shardProgress{leads: shardLeader, commit: 12}))
	}
	if _, err := coordinator(true, false).Request(ctx, "acme", "orders", validMigOpts()); err != nil {
		t.Fatalf("Request: %v", err)
	}
	if err := store.PutNodeStatus(ctx, "acme", "orders", &clustermig.NodeMigrationStatus{NodeID: "n2", Generation: 1, Phase: seal.PhaseComplete}); err != nil {
		t.Fatalf("PutNodeStatus: %v", err)
	}
	steps := func() []clustermig.NodeStep {
		t.Helper()
		got, err := store.GetMigration(ctx, "acme", "orders")
		if err != nil || got == nil {
			t.Fatalf("GetMigration = %v, %v; want the rolling record", got, err)
		}
		return got.Steps
	}

	coordinateMigrations(ctx, coordinator(true, false), store)
	if got := steps(); got[0].State != clustermig.StepRecycling {
		t.Fatalf("Steps after the system leader's tick = %+v, want n2 still recycling", got)
	}
	coordinateMigrations(ctx, coordinator(false, true), store)
	want := []clustermig.NodeStep{
		{NodeID: "n2", State: clustermig.StepServing, CatchUpIndex: 12},
		{NodeID: "n1", State: clustermig.StepRecycling},
	}
	if got := steps(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Steps after the shard leader's tick = %+v, want %+v", got, want)
	}
}

// TestRunClusterMigReconciler_StopsOnContextCancel proves the loop returns when
// its context is cancelled.
func TestRunClusterMigReconciler_StopsOnContextCancel(t *testing.T) {
	store := clustermig.NewStore(newCMMemKV())
	coord := clustermig.NewCoordinator(store, fixedResolver{owners: []string{"n1"}}, func() bool { return true }, func() int64 { return 1 })
	rec := clustermig.NewReconciler(store, "n1", noopRecycler{}, nil, func() int64 { return 1 }, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled → loop must return promptly
	done := make(chan struct{})
	go func() { runClusterMigReconciler(ctx, rec, coord, store); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runClusterMigReconciler did not return on context cancel")
	}
}

type noopRecycler struct{}

func (noopRecycler) AtTarget(*clustermig.ClusterMigration) bool                          { return false }
func (noopRecycler) Recycle(context.Context, *clustermig.ClusterMigration, []byte) error { return nil }

// TestRunClusterMigReconciler_Tick proves a running loop performs a reconcile +
// reap pass: a node owing a migration recycles + acks, and the leader then reaps
// the completed record.
func TestRunClusterMigReconciler_Tick(t *testing.T) {
	prev := clusterMigReconcileInterval
	clusterMigReconcileInterval = 20 * time.Millisecond
	defer func() { clusterMigReconcileInterval = prev }()

	store := clustermig.NewStore(newCMMemKV())
	coord := clustermig.NewCoordinator(store, fixedResolver{shard: 1, owners: []string{"n1"}},
		func() bool { return true }, func() int64 { return 1 })
	// Seed a single-owner migration this node (n1) owes.
	if _, err := coord.Request(context.Background(), "acme", "", validMigOpts()); err != nil {
		t.Fatalf("Request: %v", err)
	}
	rec := clustermig.NewReconciler(store, "n1", noopRecycler{}, nil, func() int64 { return 1 }, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runClusterMigReconciler(ctx, rec, coord, store)

	// The loop should recycle (ack PhaseComplete) then reap the completed record.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m, _ := store.GetMigration(ctx, "acme", ""); m == nil {
			return // reaped → success
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("reconciler loop did not recycle + reap the migration within timeout")
}

func validMigOpts() seal.MigrationOptions {
	return seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase, Mechanism: seal.MechanismSwap,
		Availability: seal.AvailabilityQuiesced, Credentials: seal.MustCredentials(context.Background(), []byte("pw")),
	}
}

// failingPutKV refuses every write, as a system store that has lost quorum.
type failingPutKV struct{ *cmMemKV }

func (failingPutKV) Put(context.Context, string, []byte) error { return errors.New("no quorum") }

func (failingPutKV) PutAllIfUnchanged(context.Context, string, []byte, []kvstore.KV) (bool, error) {
	return false, errors.New("no quorum")
}

// TestClusterCoordinatorMigrator_ReleasesWhenTheWriteFails proves the
// credential held for a request is released when its record is never
// written: nothing is left in memory for a migration that does not exist.
func TestClusterCoordinatorMigrator_ReleasesWhenTheWriteFails(t *testing.T) {
	ctx := context.Background()
	kv := failingPutKV{newCMMemKV()}
	coord := clustermig.NewCoordinator(clustermig.NewStore(kv), fixedResolver{shard: 1, owners: []string{"n1"}},
		func() bool { return true }, nil)
	local := clustermig.NewLocalCredentials("")
	m := &clusterCoordinatorMigrator{coord: coord, credentials: local, self: "n1"}
	if err := m.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, validMigOpts()); err == nil {
		t.Fatal("EnableEncryption succeeded with an unwritable record")
	}
	gen1 := &clustermig.ClusterMigration{Target: seal.StrategyPassphrase, Owners: []string{"n1"}, Generation: 1}
	if _, err := local.Credential(ctx, gen1); !errors.Is(err, clustermig.ErrCredentialAwaited) {
		t.Fatalf("a credential is still held for a record never written: err = %v", err)
	}
}

// fakeLeaderLocator answers systemLeaderLocator from fixed values.
type fakeLeaderLocator struct {
	leaderID  uint64
	hasLeader bool
	leaderErr error
	nodes     map[uint64]string
	memberErr error
}

var _ systemLeaderLocator = fakeLeaderLocator{}

func (f fakeLeaderLocator) GetLeaderID(uint64) (uint64, bool, error) {
	return f.leaderID, f.hasLeader, f.leaderErr
}

func (f fakeLeaderLocator) GetShardMembership(uint64) (*quicraft.Membership, error) {
	if f.memberErr != nil {
		return nil, f.memberErr
	}
	return &quicraft.Membership{Nodes: f.nodes}, nil
}

// TestClusterCoordinatorMigrator_FollowerNamesTheLeader pins the refusal a
// node that does not lead the coordinator's shard returns: the typed
// *service.NotLeaderError every transport forwards on, naming the leader, and
// never the bare clustermig sentinel, which no transport forwards and REST
// once answered with a 500 (main red, run 56290). With no leader known yet the
// refusal still has the typed shape, naming none, and no intent is written.
func TestClusterCoordinatorMigrator_FollowerNamesTheLeader(t *testing.T) {
	ctx := context.Background()
	nodes := map[uint64]string{1: "10.0.0.1:5000", 2: "10.0.0.2:5000", 3: "10.0.0.3:5000"}
	cases := map[string]struct {
		leader   systemLeaderLocator
		wantID   uint64
		wantAddr string
	}{
		"leader known":          {fakeLeaderLocator{leaderID: 3, hasLeader: true, nodes: nodes}, 3, "10.0.0.3:5000"},
		"election in progress":  {fakeLeaderLocator{nodes: nodes}, 0, ""},
		"leader lookup fails":   {fakeLeaderLocator{leaderErr: errors.New("shard not found")}, 0, ""},
		"membership read fails": {fakeLeaderLocator{leaderID: 2, hasLeader: true, memberErr: errors.New("shard not found")}, 2, ""},
		"no locator wired":      {nil, 0, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := clustermig.NewStore(newCMMemKV())
			coord := clustermig.NewCoordinator(store, fixedResolver{shard: 1, owners: []string{"n1"}},
				func() bool { return false }, nil)
			m := &clusterCoordinatorMigrator{
				coord: coord, credentials: clustermig.NewLocalCredentials(""), self: "n1",
				leader: tc.leader, leaderShard: 1,
			}
			err := m.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, validMigOpts())
			var nle *service.NotLeaderError
			if !errors.As(err, &nle) {
				t.Fatalf("EnableEncryption on a follower = %v, want *service.NotLeaderError", err)
			}
			if nle.ShardID != 1 || nle.LeaderID != tc.wantID || nle.LeaderAddress != tc.wantAddr {
				t.Fatalf("refusal = %+v, want shard 1, leader %d at %q", nle, tc.wantID, tc.wantAddr)
			}
			if rec, _ := store.GetMigration(ctx, "", ""); rec != nil {
				t.Fatalf("a follower wrote an intent record: %+v", rec)
			}
		})
	}
}
