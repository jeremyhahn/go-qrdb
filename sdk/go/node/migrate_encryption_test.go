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

package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealretire"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// noSealStoreNode boots a real standalone node and then re-adopts its seal
// subsystem through adoptSealSubsystem -- the one path both node builders
// install it by -- with the seal store absent, simulating a backend that
// cannot hold one. Every construction path in this package builds the barrier
// registry and the seal store together in buildNodeSealSubsystem, so a node
// with a registry and no store cannot currently be produced through the public
// API; going through adoptSealSubsystem rather than assigning n.sealStore
// directly is what makes this exercise the shape the field's type exists to
// prevent. If nodeSealSubsystem.sealStore were a concrete pointer again, its
// nil would arrive in n.sealStore as a non-nil interface, the precondition
// check below would fail, and without it the refusals under test would
// dereference a nil receiver instead.
func noSealStoreNode(t *testing.T) *Node {
	t.Helper()
	ctx := context.Background()

	cfg := qrdbconfig.DefaultConfig()
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(false) // not an authorization test; see testhelpers_test.go
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	n, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	store := n.sealStore
	require.NotNil(t, store, "precondition: a normal boot has a seal store")
	// The node no longer owns the store once it is re-adopted without one, so
	// the test closes it -- after the node, which it must outlive.
	t.Cleanup(func() {
		_ = n.Close()
		_ = store.Close()
	})

	n.adoptSealSubsystem(&nodeSealSubsystem{
		barrier:         n.barrier.Load(),
		credCoreSvc:     n.credCoreSvc,
		barrierRegistry: n.barrierRegistry.Load(),
		volumeLifecycle: n.volumeLifecycle,
		volumeRegistry:  n.volumeRegistry,
		openVolumes:     n.openVolumes,
		volumePoolRows:  n.volumePoolRows,
		perTenantReg:    n.perTenantReg,
	})
	// Compared with == rather than require.Nil, which reports a typed nil
	// inside a non-nil interface as nil and so cannot see the defect.
	require.True(t, n.sealStore == nil, "an absent seal store must reach Node.sealStore as a nil interface, got %T", n.sealStore)
	return n
}

func TestCanonicalRootKey(t *testing.T) {
	ctx := context.Background()
	if got := canonicalRootKeyPath(&Node{}); got != "root-key" {
		t.Errorf("default root-key path = %q", got)
	}
	cfg := &qrdbconfig.Config{}
	cfg.Seal.RootKeyPath = "custom-root"
	if got := canonicalRootKeyPath(&Node{retainedStandalone: &pendingStandaloneBoot{cfg: cfg}}); got != "custom-root" {
		t.Errorf("configured root-key path = %q", got)
	}

	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	n := &Node{sealStore: store}
	if id, err := canonicalRootKeyStrategy(ctx, n); err == nil || id != "" {
		t.Errorf("strategy with no root key = %q, %v; want an error", id, err)
	}
	if err := seal.NewStorageAdapter(store).Put(ctx, "root-key", []byte("not json")); err != nil {
		t.Fatal(err)
	}
	var syntax *json.SyntaxError
	if id, err := canonicalRootKeyStrategy(ctx, n); !errors.As(err, &syntax) || id != "" {
		t.Errorf("strategy from a corrupt root key = %q, %v; want *json.SyntaxError", id, err)
	}

	if s, err := keyedStrategyForStrategy("none", seal.Credentials{}); s != nil || !errors.Is(err, seal.ErrMigrationInvalidTarget) {
		t.Errorf("keyedStrategyForStrategy(none) = %v, %v", s, err)
	}
}

func TestEnableEncryption_RefusalsBeforeBoot(t *testing.T) {
	ctx := context.Background()
	system := service.BarrierScope{Kind: service.ScopeSystem}
	var unsupported *MigrationNotSupportedError

	n := &Node{}
	if err := n.EnableEncryption(ctx, system, seal.MigrationOptions{}); !errors.As(err, &unsupported) || !strings.Contains(unsupported.Reason, "no barrier registry") {
		t.Fatalf("EnableEncryption with no registry = %v", err)
	}
	n.barrierRegistry.Store(&seal.BarrierRegistry{})
	if err := n.EnableEncryption(ctx, system, seal.MigrationOptions{}); !errors.As(err, &unsupported) || !strings.Contains(unsupported.Reason, "not yet fully booted") {
		t.Fatalf("EnableEncryption before boot = %v", err)
	}

	if err := (&nodeEmbeddedMigrator{node: &Node{}}).EnableEncryption(ctx, system, seal.MigrationOptions{}); !errors.Is(err, service.ErrMigrationProviderUnavailable) {
		t.Fatalf("embedded migrator with no router = %v, want ErrMigrationProviderUnavailable", err)
	}

	sys := &nodeSystemMigrator{node: &Node{}}
	for name, tc := range map[string]struct {
		scope  service.BarrierScope
		reason string
	}{
		"system, no registry": {scope: system, reason: "no barrier registry"},
		"tenant":              {scope: service.BarrierScope{Kind: service.ScopeTenant}, reason: "TenantHostRegistry"},
		"unknown":             {scope: service.BarrierScope{Kind: "galaxy"}, reason: "unknown scope galaxy"},
	} {
		if err := sys.EnableEncryption(ctx, tc.scope, seal.MigrationOptions{}); !errors.As(err, &unsupported) || !strings.Contains(unsupported.Reason, tc.reason) {
			t.Errorf("%s: system migrator = %v, want a refusal naming %q", name, err, tc.reason)
		}
	}
}

func TestNodeMigrationProvider_Refusals(t *testing.T) {
	ctx := context.Background()
	system := service.BarrierScope{Kind: service.ScopeSystem}
	tenantScope := service.BarrierScope{Kind: service.ScopeTenant}
	var unsupported *MigrationNotSupportedError

	p := &nodeMigrationProvider{node: &Node{}}
	if _, err := p.MintKeyedTarget(ctx, tenantScope, seal.MigrationOptions{}); !errors.As(err, &unsupported) {
		t.Errorf("MintKeyedTarget tenant scope = %v", err)
	}
	if _, err := p.MintKeyedTarget(ctx, system, seal.MigrationOptions{TargetStrategy: "none"}); !errors.Is(err, seal.ErrMigrationInvalidTarget) {
		t.Errorf("MintKeyedTarget to a non-keyed strategy = %v, want ErrMigrationInvalidTarget", err)
	}
	if err := p.Reopen(ctx, tenantScope); !errors.As(err, &unsupported) {
		t.Errorf("Reopen tenant scope = %v", err)
	}
	if err := p.Reopen(ctx, system); !errors.As(err, &unsupported) || !strings.Contains(unsupported.Reason, "no seal store") {
		t.Errorf("Reopen with no seal store = %v", err)
	}

	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	blob, err := json.Marshal(map[string]seal.StrategyID{"strategy": seal.StrategyShamir})
	if err != nil {
		t.Fatal(err)
	}
	if err := seal.NewStorageAdapter(store).Put(ctx, "root-key", blob); err != nil {
		t.Fatal(err)
	}
	n := &Node{sealStore: store}
	p = &nodeMigrationProvider{node: n}
	var reopenErr *MigrationReopenError
	if err := p.Reopen(ctx, system); !errors.As(err, &reopenErr) {
		t.Errorf("Reopen with no retained boot params = %v, want *MigrationReopenError", err)
	}
	n.retainedStandalone = &pendingStandaloneBoot{}
	if err := p.Reopen(ctx, system); !errors.Is(err, seal.ErrShamirThresholdInvalid) {
		t.Errorf("Reopen of a Shamir commit with no recorded scheme = %v, want ErrShamirThresholdInvalid", err)
	}
}

// TestNode_EnableEncryption_NoSealStore_RefusesTyped pins the same refusal at
// the entry point a real caller reaches: nodeSystemMigrator.EnableEncryption
// refuses upfront, before Quiesce ever tears the data plane down, rather than
// only failing once canonicalRootKeyStrategy is reached deep inside a
// migration already in progress.
func TestNode_EnableEncryption_NoSealStore_RefusesTyped(t *testing.T) {
	n := noSealStoreNode(t)

	err := n.EnableEncryption(context.Background(), service.BarrierScope{Kind: service.ScopeSystem}, seal.MigrationOptions{})

	var notSupported *MigrationNotSupportedError
	require.ErrorAs(t, err, &notSupported)
	require.Contains(t, notSupported.Reason, "seal store")
	// The refusal every transport maps to 501 / Unimplemented.
	require.ErrorIs(t, err, service.ErrMigrationProviderUnavailable)
}

// TestNode_EnableEncryption_SystemNoneToPassphrase boots a real standalone node
// with a none-strategy system barrier (pebble backend), writes data, then
// enables encryption in process (online, no restart) and verifies every record
// survives and is readable through the rebuilt, now-keyed data plane.
func TestNode_EnableEncryption_SystemNoneToPassphrase(t *testing.T) {
	ctx := context.Background()

	cfg := qrdbconfig.DefaultConfig()
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(false) // not an authorization test; see testhelpers_test.go
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	// No protocol servers: drive the KV service in process.
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	n, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	require.False(t, n.IsSealed(), "none-strategy node boots unsealed")

	// --- Write data through the none (unencrypted) barrier. ----------------
	kv := n.KVService()
	require.NotNil(t, kv)
	want := map[string]string{}
	for i := 0; i < 100; i++ {
		k := fmt.Sprintf("key/%03d", i)
		v := fmt.Sprintf("value-%d", i)
		require.NoError(t, kv.Put(ctx, k, []byte(v)))
		want[k] = v
	}

	// --- Enable encryption in process (none -> passphrase). ----------------
	opts := seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(context.Background(), []byte("enable-enc-pass")),
	}
	require.NoError(t, n.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, opts))

	// --- The data plane is rebuilt and serving again under the keyed barrier.
	kv2 := n.KVService()
	require.NotNil(t, kv2, "KV service must be available after the in-process recycle")
	for k, v := range want {
		got, gerr := kv2.Get(ctx, k)
		require.NoErrorf(t, gerr, "Get(%q) after migration", k)
		require.Equalf(t, v, string(got), "value for %q after migration", k)
	}

	// --- A new write succeeds post-migration (the node is fully operational).
	require.NoError(t, kv2.Put(ctx, "post/migration", []byte("ok")))
	got, gerr := kv2.Get(ctx, "post/migration")
	require.NoError(t, gerr)
	require.Equal(t, "ok", string(got))

	// --- Restart-survival: close the node and re-open it from the SAME data
	// dir with the config STILL naming strategy "none" (the migration does not
	// rewrite the config). The boot must self-heal — registering the migrated
	// (keyed) strategy from the persisted root key — and unseal with the
	// passphrase, with all data intact.
	require.NoError(t, n.Close())

	cfg2 := qrdbconfig.DefaultConfig()
	cfg2.Auth.Enabled = qrdbconfig.BoolPtr(false) // not an authorization test; see testhelpers_test.go
	cfg2.Raft.DataDir = cfg.Raft.DataDir          // same data dir
	cfg2.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg2.Seal.Enabled = true
	cfg2.Seal.Strategy = "none" // unchanged on disk — the node must self-heal
	cfg2.Protocols.REST = false
	cfg2.Protocols.GRPC = false
	cfg2.Protocols.QUIC = false
	cfg2.Protocols.Unix = false
	cfg2.Protocols.MCP = false

	// The keyed barrier requires the passphrase to unseal on boot.
	n2, err := NewStandaloneNode(ctx, cfg2, nil, WithSealPassphrase([]byte("enable-enc-pass")))
	require.NoError(t, err, "node must boot from a migrated data dir even with config strategy=none")
	t.Cleanup(func() { _ = n2.Close() })
	require.False(t, n2.IsSealed(), "migrated node must unseal on restart")

	kv3 := n2.KVService()
	require.NotNil(t, kv3)
	for k, v := range want {
		gotR, rerr := kv3.Get(ctx, k)
		require.NoErrorf(t, rerr, "Get(%q) after restart", k)
		require.Equalf(t, v, string(gotR), "value for %q after restart", k)
	}
}

// TestNode_EnableEncryption_SealStoreSecretsSurvive: the seal store's records
// are re-encrypted by an embedded none -> passphrase enable-encryption, they
// read back before and after a restart, and no file under <dataDir>/seal
// still holds a secret's plaintext.
func TestNode_EnableEncryption_SealStoreSecretsSurvive(t *testing.T) {
	ctx := context.Background()

	newCfg := func(dataDir string) *qrdbconfig.Config {
		cfg := qrdbconfig.DefaultConfig()
		cfg.Auth.Enabled = qrdbconfig.BoolPtr(false) // not an authorization test; see testhelpers_test.go
		cfg.Raft.DataDir = dataDir
		cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
		cfg.Seal.Enabled = true
		cfg.Seal.Strategy = "none"
		cfg.Protocols.REST = false
		cfg.Protocols.GRPC = false
		cfg.Protocols.QUIC = false
		cfg.Protocols.Unix = false
		cfg.Protocols.MCP = false
		return cfg
	}
	cfg := newCfg(t.TempDir())

	n, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	secrets := map[string][]byte{
		"db-primary":  []byte("seal-store-plaintext-secret-one"),
		"api-signing": []byte("seal-store-plaintext-secret-two"),
	}
	for name, secret := range secrets {
		require.NoError(t, n.Client().SealStore().SealStorePut(ctx, &transport.SealStorePutRequest{Name: name, Secret: secret}))
	}

	opts := seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(context.Background(), []byte("enable-enc-pass")),
	}
	require.NoError(t, n.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, opts))

	readAll := func(label string, n *Node) {
		t.Helper()
		for name, secret := range secrets {
			resp, gerr := n.Client().SealStore().SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: name})
			require.NoErrorf(t, gerr, "SealStoreGet(%q) %s", name, label)
			require.Equalf(t, secret, resp.Secret, "secret %q %s", name, label)
		}
	}
	readAll("after migration", n)
	require.NoError(t, n.Close())

	n2, err := NewStandaloneNode(ctx, newCfg(cfg.Raft.DataDir), nil, WithSealPassphrase([]byte("enable-enc-pass")))
	require.NoError(t, err)
	t.Cleanup(func() { _ = n2.Close() })
	require.False(t, n2.IsSealed())
	readAll("after restart", n2)
	require.NoError(t, n2.Close())

	sealDir := filepath.Join(cfg.Raft.DataDir, "seal")
	root, err := os.OpenRoot(sealDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = root.Close() })
	files := 0
	require.NoError(t, fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, werr error) error {
		if werr != nil || d.IsDir() {
			return werr
		}
		files++
		data, rerr := fs.ReadFile(root.FS(), path)
		if rerr != nil {
			return rerr
		}
		for name, secret := range secrets {
			require.Falsef(t, bytes.Contains(data, secret), "%s still holds the plaintext of secret %q", path, name)
		}
		return nil
	}))
	require.NotZero(t, files, "the seal store has files under %s", sealDir)
}

// TestNode_EnableEncryption_RetainedScopedClientIsRefused pins that a
// database-scoped client retained from before EnableEncryption is refused
// after the migration tears the data plane down: it must not write the seal
// store through the old none barrier (overwriting the migrated ciphertext with
// plaintext the keyed barrier cannot read) nor read through it.
func TestNode_EnableEncryption_RetainedScopedClientIsRefused(t *testing.T) {
	ctx := context.Background()

	cfg := qrdbconfig.DefaultConfig()
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(false) // not an authorization test; see testhelpers_test.go
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{
		{Name: "default", Backend: string(database.BackendPebble)},
		{Name: "example", Backend: string(database.BackendPebble)},
	}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	n, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	scoped, err := n.Client().DatabaseScoped("example")
	require.NoError(t, err)
	require.NotSame(t, n.Client(), scoped, "a named database must yield a scoped client, not the primary one")

	before := []byte("seal-store-secret-before-migration")
	require.NoError(t, scoped.SealStore().SealStorePut(ctx, &transport.SealStorePutRequest{Name: "retained", Secret: before}))

	opts := seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(context.Background(), []byte("enable-enc-pass")),
	}
	require.NoError(t, n.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, opts))

	var closed *transport.ClosedError
	perr := scoped.SealStore().SealStorePut(ctx, &transport.SealStorePutRequest{Name: "retained", Secret: []byte("plaintext-after-migration")})
	require.ErrorAsf(t, perr, &closed, "SealStorePut through the retained scoped client: %v", perr)
	_, gerr := scoped.SealStore().SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "retained"})
	require.ErrorAsf(t, gerr, &closed, "SealStoreGet through the retained scoped client: %v", gerr)

	resp, err := n.Client().SealStore().SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "retained"})
	require.NoError(t, err)
	require.Equal(t, before, resp.Secret, "the retained client wrote nothing")

	fresh, err := n.Client().DatabaseScoped("example")
	require.NoError(t, err)
	resp, err = fresh.SealStore().SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "retained"})
	require.NoError(t, err)
	require.Equal(t, before, resp.Secret)
}

// failStagedDeleteBackend is a seal.StorageBackend whose Delete of a key in
// the seal store's staging keyspace fails while fail is set.
type failStagedDeleteBackend struct {
	seal.StorageBackend
	prefix string
	fail   bool
	// failJournalGet makes every read of the migration journal fail once a
	// staged delete or a root-key write has been injected.
	failJournalGet bool
	// failRootKeyPut names the canonical root-key path whose write fails.
	failRootKeyPut    string
	deleteFailed      atomic.Bool
	rootKeyPutFailed  atomic.Bool
	journalReadFailed atomic.Bool
}

var errRootKeyPut = errors.New("injected canonical root-key write failure")

func (b *failStagedDeleteBackend) Put(ctx context.Context, key string, value []byte) error {
	if b.failRootKeyPut != "" && key == b.failRootKeyPut {
		b.rootKeyPutFailed.Store(true)
		return errRootKeyPut
	}
	return b.StorageBackend.Put(ctx, key, value)
}

// errJournalRead is the injected migration-journal read fault.
var errJournalRead = errors.New("injected migration journal read fault")

func (b *failStagedDeleteBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if b.failJournalGet && (b.deleteFailed.Load() || b.rootKeyPutFailed.Load()) && strings.HasSuffix(key, ".migration") {
		b.journalReadFailed.Store(true)
		return nil, errJournalRead
	}
	return b.StorageBackend.Get(ctx, key)
}

var errStagedDelete = errors.New("injected staged-record delete failure")

func (b *failStagedDeleteBackend) Delete(ctx context.Context, key string) error {
	if b.fail && strings.HasPrefix(key, b.prefix) {
		b.deleteFailed.Store(true)
		return errStagedDelete
	}
	return b.StorageBackend.Delete(ctx, key)
}

// TestNode_EnableEncryption_StagedDeleteFault_StaysDownUntilRecovery injects
// a failure on the seal store's staged-record delete past the migration's
// commit point: the migration fails, the node serves none of the moved
// secrets rather than reopening on the adopted key, and a restart's recovery
// completes the commit so every secret reads under the new key and an update
// made after it survives a second restart.
func TestNode_EnableEncryption_StagedDeleteFault_StaysDownUntilRecovery(t *testing.T) {
	ctx := context.Background()
	f := newStagedFaultNode(t, false)
	migErr := f.migrate(t)
	require.ErrorIs(t, migErr, database.ErrMigrationRecordsPending)
	require.ErrorIs(t, migErr, errStagedDelete)
	// Reopen refused to rebuild on the adopted key: the data plane is down.
	require.Nil(t, f.n.svcNode.Load(), "the data plane must stay down while the staged commit is pending")
	require.Nil(t, f.n.Client(), "no client serves reads or writes of the moved secrets while the staged commit is pending")
	require.NoError(t, f.n.Close())

	n2 := f.reopen(t, "after the fault")
	f.requireRecovered(t, n2)
	updated := []byte("rotated-after-recovery")
	require.NoError(t, n2.Client().SealStore().SealStorePut(ctx, &transport.SealStorePutRequest{Name: "db-primary", Secret: updated}))
	require.NoError(t, n2.Close())

	n3 := f.reopen(t, "a second time")
	resp, err := n3.Client().SealStore().SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "db-primary"})
	require.NoError(t, err)
	require.Equal(t, updated, resp.Secret, "the post-recovery update must survive a second restart")
}

// TestNode_EnableEncryption_StagedDeleteAndJournalReadFault_StaysDown pins
// that Reopen fails closed when the committed journal cannot be read after a
// failed staged commit: nothing proves the records committed, so the data
// plane stays down and a restart recovers every secret.
func TestNode_EnableEncryption_StagedDeleteAndJournalReadFault_StaysDown(t *testing.T) {
	f := newStagedFaultNode(t, true)
	migErr := f.migrate(t)
	require.ErrorIs(t, migErr, database.ErrMigrationRecordsPending)
	require.Nil(t, f.n.svcNode.Load(), "the data plane must stay down while the journal is unreadable")
	require.Nil(t, f.n.Client(), "no client serves while the commit of the moved secrets is unproven")
	require.NoError(t, f.n.Close())

	f.requireRecovered(t, f.reopen(t, "after the faults"))
}

// TestNode_EnableEncryption_RootKeyAdoptFault_StaysDownUntilRecovery injects
// a failure on the canonical root-key write after the migration journal is
// committed: the node stays down rather than serving writes a restart's
// roll-forward would overwrite, and an update made after recovery survives a
// second restart.
func TestNode_EnableEncryption_RootKeyAdoptFault_StaysDownUntilRecovery(t *testing.T) {
	ctx := context.Background()
	f := newRootKeyFaultNode(t)
	migErr := f.migrate(t)
	require.ErrorIs(t, migErr, errRootKeyPut)
	strategy, err := canonicalRootKeyStrategy(ctx, f.n)
	require.NoError(t, err)
	require.Equal(t, seal.StrategyNone, strategy, "the canonical root key must still name the source strategy")
	require.Nil(t, f.n.svcNode.Load(), "the data plane must stay down while the committed migration is unadopted")
	require.Nil(t, f.n.Client(), "no client serves writes a restart's roll-forward would overwrite")
	require.NoError(t, f.n.Close())

	n2 := f.reopen(t, "after the adoption fault")
	f.requireRecovered(t, n2)
	updated := []byte("rotated-after-adoption-recovery")
	require.NoError(t, n2.Client().SealStore().SealStorePut(ctx, &transport.SealStorePutRequest{Name: "db-primary", Secret: updated}))
	require.NoError(t, n2.Close())

	n3 := f.reopen(t, "a second time")
	resp, err := n3.Client().SealStore().SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "db-primary"})
	require.NoError(t, err)
	require.Equal(t, updated, resp.Secret, "the post-recovery update must survive a second restart")
}

// TestNode_EnableEncryption_RootKeyAdoptAndJournalReadFault_StaysDown pins
// that Reopen fails closed when the canonical root key was not adopted and
// the migration journal cannot be read: nothing proves the migration did not
// commit, so the data plane stays down and a restart recovers every secret.
func TestNode_EnableEncryption_RootKeyAdoptAndJournalReadFault_StaysDown(t *testing.T) {
	ctx := context.Background()
	f := newFaultNode(t, faultOptions{failRootKeyPut: true, failJournalGet: true})
	migErr := f.migrate(t)
	require.ErrorIs(t, migErr, errRootKeyPut)
	require.True(t, f.fb.journalReadFailed.Load(), "the reopen guard must have read the journal through the fault")
	strategy, err := canonicalRootKeyStrategy(ctx, f.n)
	require.NoError(t, err)
	require.Equal(t, seal.StrategyNone, strategy, "the canonical root key must still name the source strategy")
	require.Nil(t, f.n.svcNode.Load(), "the data plane must stay down while the journal is unreadable")
	require.Nil(t, f.n.Client(), "no client serves while the migration's commit state is unknown")
	require.NoError(t, f.n.Close())

	n2 := f.reopen(t, "after the faults")
	f.requireRecovered(t, n2)
	updated := []byte("rotated-after-journal-fault-recovery")
	require.NoError(t, n2.Client().SealStore().SealStorePut(ctx, &transport.SealStorePutRequest{Name: "db-primary", Secret: updated}))
	require.NoError(t, n2.Close())

	n3 := f.reopen(t, "a second time")
	resp, err := n3.Client().SealStore().SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "db-primary"})
	require.NoError(t, err)
	require.Equal(t, updated, resp.Secret, "the post-recovery update must survive a second restart")
}

// stagedFaultNode is a standalone node whose system barrier runs over a
// failStagedDeleteBackend, holding seal-store secrets to migrate.
type stagedFaultNode struct {
	n       *Node
	fb      *failStagedDeleteBackend
	dataDir string
	secrets map[string][]byte
}

const stagedFaultPass = "staged-fault-pass"

func stagedFaultConfig(dataDir string) *qrdbconfig.Config {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(false) // not an authorization test; see testhelpers_test.go
	cfg.Raft.DataDir = dataDir
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	return cfg
}

func newStagedFaultNode(t *testing.T, failJournalGet bool) *stagedFaultNode {
	t.Helper()
	return newFaultNode(t, faultOptions{failStagedDelete: true, failJournalGet: failJournalGet})
}

// newRootKeyFaultNode builds a node whose canonical root-key write fails.
func newRootKeyFaultNode(t *testing.T) *stagedFaultNode {
	t.Helper()
	return newFaultNode(t, faultOptions{failRootKeyPut: true})
}

// faultOptions selects the faults newFaultNode injects.
type faultOptions struct {
	failStagedDelete bool
	failJournalGet   bool
	failRootKeyPut   bool
}

func newFaultNode(t *testing.T, opts faultOptions) *stagedFaultNode {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	n, err := NewStandaloneNode(ctx, stagedFaultConfig(dataDir), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	secrets := map[string][]byte{
		"db-primary":  []byte("staged-fault-secret-one"),
		"api-signing": []byte("staged-fault-secret-two"),
	}
	for name, secret := range secrets {
		require.NoError(t, n.Client().SealStore().SealStorePut(ctx, &transport.SealStorePutRequest{Name: name, Secret: secret}))
	}

	// Route the system barrier through a backend whose staged-record delete
	// fails: the same seal store, the same barrier config.
	fb := &failStagedDeleteBackend{
		StorageBackend: seal.NewStorageAdapter(n.sealStore),
		prefix:         sealretire.StagedRecords().StagingPrefix,
		fail:           opts.failStagedDelete,
		failJournalGet: opts.failJournalGet,
	}
	if opts.failRootKeyPut {
		fb.failRootKeyPut = n.barrier.Load().Config().RootKeyPath
	}
	faulty, err := seal.NewStorageBarrier(n.logger, fb, n.barrier.Load().Config(), seal.NewNoneStrategy())
	require.NoError(t, err)
	require.NoError(t, faulty.Unseal(ctx, seal.Credentials{}))
	n.barrier.Store(faulty)
	n.barrierRegistry.Load().SetSystemBarrier(faulty)
	return &stagedFaultNode{n: n, fb: fb, dataDir: dataDir, secrets: secrets}
}

func (f *stagedFaultNode) migrate(t *testing.T) error {
	t.Helper()
	opts := seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(context.Background(), []byte(stagedFaultPass)),
	}
	return f.n.EnableEncryption(context.Background(), service.BarrierScope{Kind: service.ScopeSystem}, opts)
}

func (f *stagedFaultNode) reopen(t *testing.T, label string) *Node {
	t.Helper()
	rn, err := NewStandaloneNode(context.Background(), stagedFaultConfig(f.dataDir), nil, WithSealPassphrase([]byte(stagedFaultPass)))
	require.NoErrorf(t, err, "restart %s", label)
	t.Cleanup(func() { _ = rn.Close() })
	require.False(t, rn.IsSealed())
	return rn
}

// requireRecovered asserts every secret reads back on rn and no staged copy
// is left to overwrite a later write.
func (f *stagedFaultNode) requireRecovered(t *testing.T, rn *Node) {
	t.Helper()
	ctx := context.Background()
	for name, secret := range f.secrets {
		resp, err := rn.Client().SealStore().SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: name})
		require.NoErrorf(t, err, "SealStoreGet(%q) after recovery", name)
		require.Equalf(t, secret, resp.Secret, "secret %q after recovery", name)
	}
	staged, err := seal.NewStorageAdapter(rn.sealStore).List(ctx, f.fb.prefix)
	require.NoError(t, err)
	require.Empty(t, staged, "recovery must delete every staged copy before serving")
}

// TestNode_EnableEncryption_ViaEmbeddedTransport is the go-qrdb#558 SDK-wiring
// regression test: enable-encryption must reach the SAME node-level migrator
// whether driven directly via Node.EnableEncryption (proven by
// TestNode_EnableEncryption_SystemNoneToPassphrase above) or through the
// embedded SDK transport (n.Client().Barrier().BarrierEnableEncryption).
// Before buildEmbeddedOpts wired embedded.WithEncryptionMigrator, this call
// always failed with service.ErrMigrationProviderUnavailable regardless of
// node state, since the embedded transport had no migrator wiring of any
// kind.
//
// Unlike the direct call, which runs synchronously to completion, the
// transport handler races the migration against an accept window and
// reports "migration_started" immediately (matching every other transport),
// so this test polls BarrierStatus for the strategy to change.
func TestNode_EnableEncryption_ViaEmbeddedTransport(t *testing.T) {
	ctx := context.Background()

	cfg := qrdbconfig.DefaultConfig()
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(false) // not an authorization test; see testhelpers_test.go
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	n, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	require.False(t, n.IsSealed(), "none-strategy node boots unsealed")

	kv := n.KVService()
	require.NoError(t, kv.Put(ctx, "key/embedded-transport", []byte("value")))

	// The transport acknowledges the migration and leaves it running, so the
	// test waits on the migration itself rather than polling for its effect
	// against a clock: under a loaded -race run a pebble-backed recycle can
	// outlast any fixed bound. Holding the lifecycle gate across the call
	// parks the system migration on it, which makes "queued" observable;
	// releasing hands the slot straight to the queued migration, and
	// acquiring again then returns only once it has finished.
	require.NoError(t, n.lifeGate.acquire(ctx))
	held := true
	t.Cleanup(func() {
		if held {
			n.lifeGate.release()
		}
	})

	resp, err := n.Client().Barrier().BarrierEnableEncryption(ctx, &transport.BarrierEnableEncryptionRequest{
		Strategy:   string(seal.StrategyPassphrase),
		Passphrase: []byte("enable-enc-pass-embedded"),
	})
	require.NoError(t, err, "BarrierEnableEncryption over the embedded transport")
	require.Equal(t, "migration_started", resp.Status)
	require.Equal(t, "system", resp.Scope)

	waitForQueuedTransition(t, n)
	held = false
	n.lifeGate.release()
	require.NoError(t, n.lifeGate.acquire(ctx))
	n.lifeGate.release()

	status, err := n.Client().Barrier().BarrierStatus(ctx)
	require.NoError(t, err)
	require.Equal(t, string(seal.StrategyPassphrase), status.Strategy, "strategy never changed from none to passphrase")

	// The data plane is rebuilt and serving again under the keyed barrier,
	// reached through a freshly-resolved client (the recycle replaces it).
	kv2 := n.KVService()
	require.NotNil(t, kv2, "KV service must be available after the in-process recycle")
	got, gerr := kv2.Get(ctx, "key/embedded-transport")
	require.NoError(t, gerr)
	require.Equal(t, "value", string(got))
}

// TestNode_EnableEncryption_InPlaceMechanism repeats the migration with the
// in-place (raw-file recodec) mechanism.
func TestNode_EnableEncryption_InPlaceMechanism(t *testing.T) {
	ctx := context.Background()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(false) // not an authorization test; see testhelpers_test.go
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	n, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	kv := n.KVService()
	want := map[string]string{}
	for i := 0; i < 40; i++ {
		k := fmt.Sprintf("k/%02d", i)
		v := fmt.Sprintf("v-%d", i)
		require.NoError(t, kv.Put(ctx, k, []byte(v)))
		want[k] = v
	}

	opts := seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismInPlace,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(context.Background(), []byte("inplace-pass")),
	}
	require.NoError(t, n.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, opts))

	kv2 := n.KVService()
	for k, v := range want {
		got, gerr := kv2.Get(ctx, k)
		require.NoError(t, gerr)
		require.Equal(t, v, string(got))
	}
}

// TestNode_EnableEncryption_SystemNoneToShamir proves the embedded node's
// system-scope Shamir migration produces a GENUINE M-of-N Shamir barrier —
// not a silently mislabeled passphrase barrier (the Codex #7 security
// downgrade: the embedded path used to map StrategyShamir to
// NewDeferredSoftwareStrategy + MintKeyedSibling instead of the real
// MintKeyedShamirSibling ceremony). It proves the full lifecycle: the
// migration completes, real shares are distributed to the (fake, test-only)
// CeremonyDistributor, the system barrier reports strategy=shamir with the
// correct threshold/total and comes back SEALED (Shamir never auto-unseals),
// the data plane correctly stays torn down until a Security Officer quorum
// unseals it, and submitting that quorum completes the boot with zero data
// loss.
func TestNode_EnableEncryption_SystemNoneToShamir(t *testing.T) {
	ctx := context.Background()

	cfg := qrdbconfig.DefaultConfig()
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(false) // not an authorization test; see testhelpers_test.go
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	n, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	require.False(t, n.IsSealed(), "none-strategy node boots unsealed")

	// Wire a fake CeremonyDistributor directly on the live system barrier
	// (white-box: this test lives in package node) so the migration's real
	// M-of-N split + distribution can be captured and later replayed as a
	// genuine quorum, without standing up the full external-officer PKI
	// ceremony. This is the same operational precondition a fresh
	// BarrierShamirInitialize has: SetCeremonyDistributor before the
	// migration, exactly mirroring pkg/server/tenant_migrate_test.go's
	// TestTenantHostRegistry_MigrateEncryption_NoneToShamir.
	custody := sealtest.NewCustody("h1", "h2", "h3")
	custody.Install(n.barrier.Load())

	// --- Write data through the none (unencrypted) barrier. ----------------
	kv := n.KVService()
	require.NotNil(t, kv)
	want := map[string]string{}
	for i := 0; i < 25; i++ {
		k := fmt.Sprintf("shamir/%03d", i)
		v := fmt.Sprintf("value-%d", i)
		require.NoError(t, kv.Put(ctx, k, []byte(v)))
		want[k] = v
	}

	// --- Enable encryption in process (none -> shamir). ---------------------
	opts := seal.MigrationOptions{
		TargetStrategy:  seal.StrategyShamir,
		Mechanism:       seal.MechanismSwap,
		Availability:    seal.AvailabilityQuiesced,
		ShamirThreshold: 2,
		ShamirTotal:     3,
	}
	require.NoError(t, n.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, opts))

	// A genuine M-of-N ceremony ran: 3 real shares were distributed (not a
	// mislabeled passphrase strategy with zero officer shares).
	for _, h := range []string{"h1", "h2", "h3"} {
		require.NotEmptyf(t, custody.Distributor.Share(seal.DomainUnseal, h), "migration must distribute a real Shamir share to holder %s", h)
	}

	// The system barrier is now genuinely Shamir — correct label, correct
	// M/N — and SEALED (Shamir never auto-unseals from disk, migration or
	// not).
	require.True(t, n.IsSealed(), "post-migration shamir system barrier must be sealed pending Security Officer quorum")
	status := n.barrier.Load().Status()
	require.Equal(t, seal.StrategyShamir, status.Strategy, "must not be silently downgraded to passphrase")
	require.Equal(t, 2, status.ShamirThreshold)
	require.Equal(t, 3, status.ShamirTotal)

	// The data plane correctly stays torn down: no KV service until unsealed.
	require.Nil(t, n.KVService(), "data plane must not be rebuilt while the migrated shamir barrier is sealed")

	// Submitting a quorum (2-of-3) of the REAL distributed shares unseals the
	// migrated barrier — the "shares are usable" proof.
	// The reopened barrier is re-composed with the node's own ceremony
	// service as its custody; put the test's custody back so its holders can
	// answer.
	custody.Install(n.barrier.Load())
	progress, uerr := custody.Quorum(ctx, n.barrier.Load(), seal.DomainUnseal, seal.OpUnseal, "h1", "h2")
	require.NoError(t, uerr)
	require.True(t, progress.Complete)
	require.False(t, n.barrier.Load().IsSealed())

	// Completing the boot (mirrors the ceremony API driving Node.Unseal once
	// quorum is reached externally — see its doc comment on the "already
	// unsealed externally" fallthrough) rebuilds the data plane, and every
	// datum written before the migration survives with zero loss.
	require.NoError(t, n.Unseal(ctx, seal.Credentials{}))
	require.False(t, n.IsSealed())

	kv2 := n.KVService()
	require.NotNil(t, kv2, "KV service must be available after the SO quorum completes the boot")
	for k, v := range want {
		got, gerr := kv2.Get(ctx, k)
		require.NoErrorf(t, gerr, "Get(%q) after shamir migration+unseal", k)
		require.Equalf(t, v, string(got), "value for %q after shamir migration+unseal (DATA LOSS)", k)
	}

	// A new write succeeds post-migration (the node is fully operational).
	require.NoError(t, kv2.Put(ctx, "post/shamir-migration", []byte("ok")))
	got, gerr := kv2.Get(ctx, "post/shamir-migration")
	require.NoError(t, gerr)
	require.Equal(t, "ok", string(got))
}

// TestNode_EnableEncryption_SystemShamirInsufficientOfficers proves a Shamir
// target migration on a node with no enrolled Security Officers fails CLOSED
// through the real ceremony's own precondition — the same failure mode a
// fresh BarrierShamirInitialize would give — rather than silently completing
// as a mislabeled passphrase barrier via the generic MintKeyedSibling path
// (the routing this test guards against regressing to).
func TestNode_EnableEncryption_SystemShamirInsufficientOfficers(t *testing.T) {
	ctx := context.Background()

	cfg := qrdbconfig.DefaultConfig()
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(false) // not an authorization test; see testhelpers_test.go
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	n, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	// No officers enrolled: the node's real ceremony service is wired
	// automatically by lifecycle.BuildServices (SetCeremonyDistributor), but
	// with zero Security Officers registered the ceremony's own
	// insufficient-officers guard applies. This still proves the routing fix:
	// the OLD buggy code reached MintKeyedSibling with an empty-secret
	// passphrase strategy (a different, wrong failure mode, or a silent
	// mislabeled "success"); the FIXED code reaches MintKeyedShamirSibling's
	// real ceremony precondition instead.

	opts := seal.MigrationOptions{
		TargetStrategy:  seal.StrategyShamir,
		Mechanism:       seal.MechanismSwap,
		Availability:    seal.AvailabilityQuiesced,
		ShamirThreshold: 2,
		ShamirTotal:     3,
	}
	err = n.EnableEncryption(ctx, service.BarrierScope{Kind: service.ScopeSystem}, opts)
	require.Error(t, err)

	// The barrier must be untouched by the failed attempt.
	require.False(t, n.IsSealed())
	require.Equal(t, seal.StrategyNone, n.barrier.Load().ActiveStrategy())
}
