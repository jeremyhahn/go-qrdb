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
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
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
func (k *cmMemKV) Delete(_ context.Context, key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, key)
	return nil
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

// newP256 returns a fresh ECDSA P-256 key for HPKE round-trip tests.
func newP256(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return priv
}

// TestDaemonCredential_SealOpenRoundTrip proves the sealer seals the credential
// to the local node's key and the opener recovers exactly the plaintext — the
// in-record HPKE envelope flow (self path), for both system and tenant scopes.
func TestDaemonCredential_SealOpenRoundTrip(t *testing.T) {
	ctx := context.Background()
	priv := newP256(t)
	const node = "127.0.0.1:7000"

	recv, err := keydelivery.NewReceiver(priv, node, keydelivery.WithTTL(migrationCredentialTTL))
	if err != nil {
		t.Fatalf("NewReceiver: %v", err)
	}
	sealer := &daemonCredentialSealer{selfNode: node, selfPub: &priv.PublicKey}
	opener := &daemonCredentialOpener{receiver: recv, selfNode: node}

	for _, m := range []*clustermig.ClusterMigration{
		{ShardID: 1, Target: seal.StrategyPassphrase, Owners: []string{node}},                                    // system scope
		{TenantID: "acme", Database: "db", ShardID: 42, Target: seal.StrategyPassphrase, Owners: []string{node}}, // tenant scope
	} {
		secret := []byte("operator-unlock-secret")
		sealed, serr := sealer.SealFor(ctx, m, m.Owners, secret)
		if serr != nil {
			t.Fatalf("SealFor: %v", serr)
		}
		if blob, ok := sealed[node]; !ok || len(blob) == 0 {
			t.Fatalf("no sealed envelope for %s", node)
		}
		// The plaintext must not appear in the sealed blob.
		if strings.Contains(string(sealed[node]), string(secret)) {
			t.Fatal("plaintext credential leaked into the sealed envelope")
		}
		m.Credentials = sealed
		got, oerr := opener.Open(ctx, m)
		if oerr != nil {
			t.Fatalf("Open: %v", oerr)
		}
		if string(got) != string(secret) {
			t.Fatalf("round-trip credential = %q, want %q", got, secret)
		}
	}
}

// TestDaemonCredentialOpener_Absent covers the no-credential and no-receiver
// short-circuits.
func TestDaemonCredentialOpener_Absent(t *testing.T) {
	ctx := context.Background()
	priv := newP256(t)
	recv, _ := keydelivery.NewReceiver(priv, "n1", keydelivery.WithTTL(time.Hour))

	o := &daemonCredentialOpener{receiver: recv, selfNode: "n1"}
	if got, err := o.Open(ctx, &clustermig.ClusterMigration{}); got != nil || err != nil {
		t.Fatalf("Open(no creds) = %v, %v; want nil, nil", got, err)
	}
	if got, err := o.Open(ctx, &clustermig.ClusterMigration{Credentials: map[string][]byte{"other": {1}}}); got != nil || err != nil {
		t.Fatalf("Open(no entry for self) = %v, %v; want nil, nil", got, err)
	}
	noRecv := &daemonCredentialOpener{selfNode: "n1"}
	if got, err := noRecv.Open(ctx, &clustermig.ClusterMigration{Credentials: map[string][]byte{"n1": {1}}}); got != nil || err != nil {
		t.Fatalf("Open(no receiver) = %v, %v; want nil, nil", got, err)
	}
}

// TestMigrationCredMeta covers the scope→meta mapping (system sentinels vs
// real tenant/db/shard).
func TestMigrationCredMeta(t *testing.T) {
	sys := migrationCredMeta(&clustermig.ClusterMigration{}, "n1")
	if sys.TenantID != systemCredMetaTenant || sys.Database != systemCredMetaDatabase || sys.ShardID != 1 || sys.TargetNodeID != "n1" {
		t.Fatalf("system meta = %+v", sys)
	}
	ten := migrationCredMeta(&clustermig.ClusterMigration{TenantID: "acme", Database: "db", ShardID: 9}, "n2")
	if ten.TenantID != "acme" || ten.Database != "db" || ten.ShardID != 9 || ten.TargetNodeID != "n2" {
		t.Fatalf("tenant meta = %+v", ten)
	}
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

// TestDaemonLocalRecycler_AlreadyAtTarget proves the idempotency skip: when the
// local barrier already reports the target strategy, Recycle is a no-op (the
// migrator is never invoked) — the post-reboot/re-issue resume path.
func TestDaemonLocalRecycler_AlreadyAtTarget(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.DiscardHandler)

	// A keyed (software/passphrase) system barrier registry.
	strategy, err := seal.NewSoftwareStrategy([]byte("pw"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	sb, err := seal.NewStorageBarrier(logger, seal.NewMemoryBackend(), seal.BarrierConfig{RootKeyPath: "system/root-key"}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := sb.Initialize(ctx, seal.MustCredentials([]byte("pw"))); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	reg, err := seal.NewBarrierRegistry(logger, sb)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	mig := &recordingMigrator{}
	r := &daemonLocalRecycler{migrator: mig, barrierReg: reg}
	// Target == the barrier's current strategy → already-at-target → no-op.
	target := sb.Status().Strategy
	if err := r.Recycle(ctx, &clustermig.ClusterMigration{Target: target}, []byte("x")); err != nil {
		t.Fatalf("Recycle(already-at-target): %v", err)
	}
	if mig.scope.Kind != "" {
		t.Fatal("migrator must not be invoked when already at the target strategy")
	}
	// A DIFFERENT target → not at target → migrator invoked.
	r.Recycle(ctx, &clustermig.ClusterMigration{Target: seal.StrategyShamir}, []byte("x"))
	if mig.scope.Kind != service.ScopeSystem {
		t.Fatal("migrator should run when the target differs from the current strategy")
	}
}

// fixedResolver / fixedSealer satisfy the clustermig seams for the coordinator
// migrator test.
type fixedResolver struct {
	shard  uint64
	owners []string
}

func (f fixedResolver) Owners(context.Context, string, string) (uint64, []string, error) {
	return f.shard, f.owners, nil
}

type passthroughSealer struct{}

func (passthroughSealer) SealFor(_ context.Context, _ *clustermig.ClusterMigration, owners []string, cred []byte) (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, o := range owners {
		out[o] = append([]byte(nil), cred...)
	}
	return out, nil
}

// TestClusterCoordinatorMigrator proves the transport migrator writes a
// replicated intent for system and tenant scope.
func TestClusterCoordinatorMigrator(t *testing.T) {
	ctx := context.Background()
	store := clustermig.NewStore(newCMMemKV())
	coord := clustermig.NewCoordinator(store, fixedResolver{shard: 1, owners: []string{"n1"}},
		passthroughSealer{}, func() bool { return true }, func() int64 { return 1 })
	m := &clusterCoordinatorMigrator{coord: coord}

	opts := seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase, Mechanism: seal.MechanismSwap,
		Availability: seal.AvailabilityQuiesced, Credentials: seal.MustCredentials([]byte("pw")),
	}
	if err := m.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, opts); err != nil {
		t.Fatalf("EnableEncryption(system): %v", err)
	}
	if got, _ := store.GetMigration(ctx, "", ""); got == nil {
		t.Fatal("system migration intent was not written")
	}
	if err := m.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeTenant, TenantID: "acme"}, opts); err != nil {
		t.Fatalf("EnableEncryption(tenant): %v", err)
	}
	if got, _ := store.GetMigration(ctx, "acme", ""); got == nil {
		t.Fatal("tenant migration intent was not written")
	}
}

// TestReapCompletedMigrations proves the leader reaps a completed migration and
// leaves an incomplete one in place.
func TestReapCompletedMigrations(t *testing.T) {
	ctx := context.Background()
	store := clustermig.NewStore(newCMMemKV())
	coord := clustermig.NewCoordinator(store, fixedResolver{shard: 1, owners: []string{"n1"}},
		passthroughSealer{}, func() bool { return true }, func() int64 { return 1 })

	// Incomplete migration: not reaped.
	if _, err := coord.Request(ctx, "acme", "", validMigOpts(), []byte("pw")); err != nil {
		t.Fatalf("Request: %v", err)
	}
	reapCompletedMigrations(ctx, coord, store)
	if got, _ := store.GetMigration(ctx, "acme", ""); got == nil {
		t.Fatal("incomplete migration must not be reaped")
	}
	// Complete it → reaped.
	if err := store.PutNodeStatus(ctx, "acme", "", &clustermig.NodeMigrationStatus{NodeID: "n1", Generation: 1, Phase: seal.PhaseComplete}); err != nil {
		t.Fatalf("PutNodeStatus: %v", err)
	}
	reapCompletedMigrations(ctx, coord, store)
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

// TestRunClusterMigReconciler_StopsOnContextCancel proves the loop returns when
// its context is cancelled.
func TestRunClusterMigReconciler_StopsOnContextCancel(t *testing.T) {
	store := clustermig.NewStore(newCMMemKV())
	coord := clustermig.NewCoordinator(store, fixedResolver{owners: []string{"n1"}}, passthroughSealer{}, func() bool { return true }, func() int64 { return 1 })
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
		passthroughSealer{}, func() bool { return true }, func() int64 { return 1 })
	// Seed a single-owner migration this node (n1) owes.
	if _, err := coord.Request(context.Background(), "acme", "", validMigOpts(), []byte("pw")); err != nil {
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
		Availability: seal.AvailabilityQuiesced, Credentials: seal.MustCredentials([]byte("pw")),
	}
}
