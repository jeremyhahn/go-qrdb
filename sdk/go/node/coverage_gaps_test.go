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
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
	"github.com/jeremyhahn/go-qrdb/pkg/volume/volumetest"
)

// newBareStandaloneNode starts an in-memory standalone node with every
// network protocol off.
func newBareStandaloneNode(t *testing.T) *Node {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST, cfg.Protocols.GRPC, cfg.Protocols.QUIC = false, false, false
	cfg.Protocols.Unix, cfg.Protocols.MCP = false, false
	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("NewStandaloneNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

// A node that holds a service principal but has no server -- it is
// recycling its data plane -- refuses a delegation with the retryable
// recycling error rather than presenting it.
func TestDelegatedContext_RefusedWhileRecycling(t *testing.T) {
	n := &Node{servicePrincipal: "billing"}
	ctx, err := n.DelegatedContext(context.Background(), nil)
	var recycling *service.DataPlaneRecyclingError
	if ctx != nil || !errors.As(err, &recycling) || recycling.Service != "delegated context" {
		t.Fatalf("DelegatedContext = %v, %v; want *service.DataPlaneRecyclingError for the delegated context", ctx, err)
	}
	if errors.Is(err, ErrNoServicePrincipal) {
		t.Fatal("a recycling refusal must not read as a missing service principal")
	}
}

func TestNodeErrors_MessagesAndCauses(t *testing.T) {
	cause := errors.New("boom")
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"storage":         {&StorageCreateError{Backend: "pebble", Err: cause}, "node: failed to create storage backend pebble: boom"},
		"engine":          {&EngineCreateError{Engine: "mvcc", Err: cause}, "node: failed to create engine mvcc: boom"},
		"services":        {&ServicesCreateError{Err: cause}, "node: failed to build services: boom"},
		"server":          {&ServerCreateError{Err: cause}, "node: failed to start server: boom"},
		"host":            {&HostCreateError{Err: cause}, "node: failed to create clustered host: boom"},
		"config":          {&ConfigError{Field: "raft.data_dir", Message: "is required", Err: cause}, "node: config error: raft.data_dir is required"},
		"client":          {&ClientCreateError{Err: cause}, "node: failed to create SDK client: boom"},
		"unseal":          {&UnsealError{Err: cause}, "node: failed to unseal barrier: boom"},
		"login":           {&LoginWiringError{Err: cause}, "node: failed to wire unified-login ceremony: boom"},
		"auth factor":     {&AuthFactorWiringError{Err: cause}, "node: failed to wire AuthFactorService from FactorRegistry: boom"},
		"pending cert":    {&PendingAdminCertWriteError{Err: cause}, "node: failed to record the pending admin certificate: boom"},
		"tenant infra":    {&TenantInfraError{Err: cause}, "node: failed to build tenant infrastructure: boom"},
		"abac":            {&ABACWiringError{Err: cause}, "node: failed to wire ABAC authorization decorator: boom"},
		"auth middleware": {&AuthMiddlewareWiringError{Err: cause}, "node: failed to wire auth middleware for the multi-protocol server: boom"},
		"cluster join":    {&ClusterJoinError{Err: cause}, "node: cluster join: boom"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
			if !errors.Is(tc.err, cause) {
				t.Errorf("errors.Is(%T, cause) = false; the cause must unwrap", tc.err)
			}
		})
	}
}

// refusingVolumeKeyProvider is a volume.KeyProvider that never hands out a key.
type refusingVolumeKeyProvider struct{}

var errVolumeKeyRefused = errors.New("volume key refused")

func (refusingVolumeKeyProvider) VolumeKey(context.Context, string, string, string) (volume.VolumeKey, error) {
	return volume.VolumeKey{}, errVolumeKeyRefused
}

var _ volume.KeyProvider = refusingVolumeKeyProvider{}

func TestNodeOptions_RecordTheirSeams(t *testing.T) {
	var o nodeOptions
	WithAuthorizerWrapper(func(inner auth.Authorizer) auth.Authorizer { return inner })(&o)
	if o.authorizerWrapper == nil || o.authorizerWrapper(nil) != nil {
		t.Fatal("WithAuthorizerWrapper did not record the wrapper")
	}

	WithVolumeKeyProvider(refusingVolumeKeyProvider{})(&o)
	if _, ok := o.volumeKeyProvider.(refusingVolumeKeyProvider); !ok {
		t.Fatalf("WithVolumeKeyProvider recorded %T", o.volumeKeyProvider)
	}
	WithVolumeKeyProvider(nil)(&o)
	if _, ok := o.volumeKeyProvider.(refusingVolumeKeyProvider); !ok {
		t.Fatal("WithVolumeKeyProvider(nil) replaced the configured provider")
	}

	WithABACPolicyDir("/etc/qrdb/abac")(&o)
	if o.abacPolicyDir != "/etc/qrdb/abac" {
		t.Fatalf("abacPolicyDir = %q", o.abacPolicyDir)
	}
}

func TestClusterTrustDomain(t *testing.T) {
	cfg := &qrdbconfig.Config{}
	if got := clusterTrustDomain(cfg); got != qrdbconfig.DefaultClusterTrustDomain {
		t.Errorf("default trust domain = %q, want %q", got, qrdbconfig.DefaultClusterTrustDomain)
	}
	cfg.Auth.ClusterTrustDomain = "db.example.com"
	if got := clusterTrustDomain(cfg); got != "db.example.com" {
		t.Errorf("configured trust domain = %q", got)
	}
}

// The node's registry resolves every service family through the live
// server's registry, so it answers exactly what that registry answers.
func TestNodeServiceRegistry_DelegatesEveryFamily(t *testing.T) {
	n := newBareStandaloneNode(t)
	r := &nodeServiceRegistry{node: n}
	direct := n.srv.Load().ServiceRegistry()
	if direct == nil {
		t.Fatal("a started standalone node has no service registry")
	}
	const tenant, db = "acme", "absent"
	resolvers := map[string][2]func() error{
		"KV":          {func() error { _, err := r.KVService(tenant, db); return err }, func() error { _, err := direct.KVService(tenant, db); return err }},
		"MVCC":        {func() error { _, err := r.MVCCService(tenant, db); return err }, func() error { _, err := direct.MVCCService(tenant, db); return err }},
		"Watch":       {func() error { _, err := r.WatchService(tenant, db); return err }, func() error { _, err := direct.WatchService(tenant, db); return err }},
		"Lease":       {func() error { _, err := r.LeaseService(tenant, db); return err }, func() error { _, err := direct.LeaseService(tenant, db); return err }},
		"Index":       {func() error { _, err := r.IndexService(tenant, db); return err }, func() error { _, err := direct.IndexService(tenant, db); return err }},
		"TimeSeries":  {func() error { _, err := r.TimeSeriesService(tenant, db); return err }, func() error { _, err := direct.TimeSeriesService(tenant, db); return err }},
		"Vector":      {func() error { _, err := r.VectorService(tenant, db); return err }, func() error { _, err := direct.VectorService(tenant, db); return err }},
		"Transaction": {func() error { _, err := r.TransactionService(tenant, db); return err }, func() error { _, err := direct.TransactionService(tenant, db); return err }},
		"Batch":       {func() error { _, err := r.BatchService(tenant, db); return err }, func() error { _, err := direct.BatchService(tenant, db); return err }},
		"Table":       {func() error { _, err := r.TableService(tenant, db); return err }, func() error { _, err := direct.TableService(tenant, db); return err }},
		"Sequence":    {func() error { _, err := r.SequenceService(tenant, db); return err }, func() error { _, err := direct.SequenceService(tenant, db); return err }},
		"Privilege":   {func() error { _, err := r.PrivilegeService(tenant, db); return err }, func() error { _, err := direct.PrivilegeService(tenant, db); return err }},
		"Collection":  {func() error { _, err := r.CollectionService(tenant, db); return err }, func() error { _, err := direct.CollectionService(tenant, db); return err }},
		"Lock":        {func() error { _, err := r.LockService(tenant, db); return err }, func() error { _, err := direct.LockService(tenant, db); return err }},
	}
	for name, pair := range resolvers {
		via, want := pair[0](), pair[1]()
		if via == nil || want == nil || via.Error() != want.Error() {
			t.Errorf("%s for an absent database: via node = %v, direct = %v; want the same refusal", name, via, want)
		}
	}
	var wantRefusal error
	if refuser, ok := direct.(service.StreamRefuser); ok {
		wantRefusal = refuser.StreamRefusal(tenant, db)
	}
	if got := r.StreamRefusal(tenant, db); (got == nil) != (wantRefusal == nil) || (got != nil && got.Error() != wantRefusal.Error()) {
		t.Errorf("StreamRefusal = %v, want %v", got, wantRefusal)
	}
}

// A standalone server has no replica forwarding configured, so the
// embedded forwarder declines rather than forwarding anywhere.
func TestNodeReplicaForwarder_StandaloneServerDeclines(t *testing.T) {
	n := newBareStandaloneNode(t)
	f := &nodeReplicaForwarder{node: n}
	if _, _, ok := f.Forward(context.Background(), nil, "kv.Put", "default", nil, &placement.NotAReplicaError{}); ok {
		t.Fatal("Forward forwarded on a standalone server")
	}
	if _, _, ok := f.ForwardToLeader(context.Background(), nil, "kv.Put", "default", nil, "127.0.0.1:1"); ok {
		t.Fatal("ForwardToLeader forwarded on a standalone server")
	}
	idle := &nodeReplicaForwarder{node: &Node{}}
	if _, _, ok := idle.ForwardToLeader(context.Background(), nil, "kv.Put", "default", nil, "127.0.0.1:1"); ok {
		t.Fatal("ForwardToLeader forwarded with no server")
	}
}

func TestPendingAdminCertStore_DeleteRemovesTheRecord(t *testing.T) {
	n := newBareStandaloneNode(t)
	store := PendingAdminCertStore(n.Services().KV)
	ctx := context.Background()
	if err := store.Put(ctx, "admin", []byte("pem")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, err := store.Get(ctx, "admin"); err != nil || string(got) != "pem" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if err := store.Delete(ctx, "admin"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	var notFound *service.KeyNotFoundError
	if got, err := store.Get(ctx, "admin"); !errors.As(err, &notFound) {
		t.Fatalf("Get after Delete = %q, %v; want *service.KeyNotFoundError", got, err)
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

// stubVolume is a volume.Volume whose Usage and Close fail on demand.
type stubVolume struct {
	root     string
	usageErr error
	closeErr error
	health   volume.Health
	closed   bool
}

func (v *stubVolume) Root() string { return v.root }

func (v *stubVolume) Usage(context.Context) (volume.Usage, error) {
	return volume.Usage{}, v.usageErr
}

func (v *stubVolume) Health(context.Context) volume.Health { return v.health }

func (v *stubVolume) Close(context.Context) error {
	if v.closed {
		return nil
	}
	v.closed = true
	return v.closeErr
}

var _ volume.Volume = (*stubVolume)(nil)

func TestOpenConfiguredVolumes_RecordsEveryFailure(t *testing.T) {
	ctx := context.Background()
	registry, err := newVolumeRegistry()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dup := t.TempDir()
	pools := []qrdbconfig.VolumePoolConfig{
		{Name: "unknown", Provider: "no-such-provider", Path: t.TempDir()},
		{Name: "file", Path: file},
		{Name: "dup", Path: dup},
		{Name: "dup", Path: dup},
	}
	open, rows := openConfiguredVolumes(ctx, "node-1", pools, registry, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { closeOpenedVolumes(ctx, rows, slog.New(slog.DiscardHandler)) })
	if len(rows) != len(pools) {
		t.Fatalf("rows = %d, want %d", len(rows), len(pools))
	}
	for i, want := range []struct {
		provider string
		opened   bool
	}{{"no-such-provider", false}, {"dir", false}, {"dir", true}, {"dir", false}} {
		row := rows[i]
		if row.provider != want.provider || (row.vol != nil) != want.opened || (row.err == nil) != want.opened {
			t.Errorf("row %d (%s) = provider %q, vol %v, err %v; want provider %q, opened %v", i, row.name, row.provider, row.vol, row.err, want.provider, want.opened)
		}
	}
	if got := open.Pools(); len(got) != 1 || got[0] != "dup" {
		t.Errorf("open pools = %v, want only the first dup", got)
	}
}

func TestCheckProviderConsistency_EvictsAMismatchedPool(t *testing.T) {
	ctx := context.Background()
	errMissing := errors.New("not in the catalog")
	catalog := &volumetest.Service{T: t, GetPoolFn: func(_ context.Context, name string) (*volume.PoolDetail, error) {
		if name == "uncataloged" {
			return nil, errMissing
		}
		return &volume.PoolDetail{PoolView: volume.PoolView{Provider: "lvm"}}, nil
	}}
	open := volume.NewOpenVolumes("node-1")
	matching, uncataloged, mismatched := &stubVolume{root: t.TempDir()}, &stubVolume{root: t.TempDir()}, &stubVolume{root: t.TempDir()}
	for name, v := range map[string]volume.Volume{"matching": matching, "uncataloged": uncataloged, "mismatched": mismatched} {
		if err := open.Add(name, v); err != nil {
			t.Fatal(err)
		}
	}
	rows := []openedVolumePool{
		{name: "failed", provider: "dir", err: errMissing},
		{name: "uncataloged", provider: "dir", vol: uncataloged},
		{name: "matching", provider: "lvm", vol: matching},
		{name: "mismatched", provider: "dir", vol: mismatched},
	}
	errs := checkProviderConsistency(ctx, catalog, open, rows)
	var mismatch *volume.ProviderMismatchError
	if len(errs) != 1 || !errors.As(errs[0], &mismatch) || mismatch.Pool != "mismatched" || mismatch.CatalogProvider != "lvm" || mismatch.ConfiguredProvider != "dir" {
		t.Fatalf("errs = %v; want one *volume.ProviderMismatchError for mismatched", errs)
	}
	if !mismatched.closed || matching.closed || uncataloged.closed {
		t.Errorf("closed: mismatched %v, matching %v, uncataloged %v; want only mismatched", mismatched.closed, matching.closed, uncataloged.closed)
	}
	if got := open.Pools(); len(got) != 2 {
		t.Errorf("open pools after the check = %v, want matching and uncataloged", got)
	}
}

func TestCloseOpenedVolumes_LogsACloseFailure(t *testing.T) {
	var logs bytes.Buffer
	failing := &stubVolume{closeErr: errors.New("device busy")}
	closing := &stubVolume{}
	closeOpenedVolumes(context.Background(), []openedVolumePool{
		{name: "unopened", err: errors.New("never opened")},
		{name: "busy", vol: failing},
		{name: "fine", vol: closing},
	}, slog.New(slog.NewTextHandler(&logs, nil)))
	if !failing.closed || !closing.closed {
		t.Fatal("an opened volume was not closed")
	}
	if out := logs.String(); !strings.Contains(out, "failed to close volume") || !strings.Contains(out, "pool=busy") || strings.Contains(out, "pool=fine") {
		t.Errorf("logs = %q; want one close failure, for busy", out)
	}
}

func TestBuildNodeReport(t *testing.T) {
	errOpen := errors.New("device not present")
	errUsage := errors.New("statfs failed")
	report := buildNodeReport(context.Background(), "node-1", []openedVolumePool{
		{name: "down", err: errOpen},
		{name: "unmeasured", vol: &stubVolume{usageErr: errUsage, health: volume.Health{State: volume.StateReady}}},
		{name: "explained", vol: &stubVolume{usageErr: errUsage, health: volume.Health{State: volume.StateLocked, Detail: "locked"}}},
	})
	if report.NodeID != "node-1" || len(report.Pools) != 3 {
		t.Fatalf("report = %+v", report)
	}
	if down := report.Pools["down"]; !down.Mapped || down.Health.State != volume.StateUnavailable || down.Health.Detail != errOpen.Error() {
		t.Errorf("down = %+v", down)
	}
	if got := report.Pools["unmeasured"].Health; got.State != volume.StateReady || got.Detail != errUsage.Error() {
		t.Errorf("unmeasured health = %+v; want the usage failure as its detail", got)
	}
	if got := report.Pools["explained"].Health; got.Detail != "locked" {
		t.Errorf("explained health = %+v; a provider detail must not be overwritten", got)
	}
}

// blockDatabasesDir puts a regular file where the database manager expects
// its databases directory, so OpenAll cannot read it.
func blockDatabasesDir(t *testing.T, dataDir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dataDir, "databases"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// wantDatabaseManagerOpenError asserts err is the typed refusal a node
// returns when its database manager cannot read the databases directory.
func wantDatabaseManagerOpenError(t *testing.T, n *Node, err error) {
	t.Helper()
	var create *StorageCreateError
	var meta *database.MetaReadError
	if n != nil || !errors.As(err, &create) || create.Backend != "database-manager" || !errors.As(err, &meta) {
		t.Fatalf("node = %v, err = %v; want *StorageCreateError for the database manager wrapping *database.MetaReadError", n, err)
	}
}

func TestNewStandaloneNode_DatabaseManagerOpenFailure(t *testing.T) {
	t.Run("unsealed", func(t *testing.T) {
		dataDir := t.TempDir()
		cfg := qrdbconfig.DefaultConfig()
		cfg.Raft.DataDir = dataDir
		cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
		cfg.Protocols.REST, cfg.Protocols.GRPC, cfg.Protocols.QUIC = false, false, false
		cfg.Protocols.Unix, cfg.Protocols.MCP = false, false
		blockDatabasesDir(t, dataDir)
		n, err := NewStandaloneNode(context.Background(), cfg, nil)
		wantDatabaseManagerOpenError(t, n, err)
	})
	t.Run("auto-unsealed", func(t *testing.T) {
		dataDir := t.TempDir()
		blockDatabasesDir(t, dataDir)
		n, err := NewStandaloneNode(context.Background(), sealedStandaloneConfig(t, dataDir), nil, WithSealPassphrase([]byte("correct horse")))
		wantDatabaseManagerOpenError(t, n, err)
	})
}

// A malformed auth setting fails boot at the step that first parses it --
// the service build for the audit and binding policies, the server wiring
// (the last step) for the revocation cache TTL -- and the node tears down
// everything it built before returning the typed error.
func TestNewStandaloneNode_MalformedAuthSettingFailsBoot(t *testing.T) {
	isServices := func(err error) bool { var e *ServicesCreateError; return errors.As(err, &e) }
	isWiring := func(err error) bool { var e *AuthMiddlewareWiringError; return errors.As(err, &e) }
	for name, tc := range map[string]struct {
		set  func(*qrdbconfig.Config)
		want func(error) bool
	}{
		"revocation cache ttl": {func(c *qrdbconfig.Config) { c.Auth.RevocationCacheTTL = "fortnightly" }, isWiring},
		"audit failure policy": {func(c *qrdbconfig.Config) { c.Auth.AuditFailurePolicy = "shrug" }, isServices},
		"mtls binding policy":  {func(c *qrdbconfig.Config) { c.Auth.MTLSBindingPolicy = "sometimes" }, isServices},
	} {
		t.Run(name+"/unsealed", func(t *testing.T) {
			cfg := qrdbconfig.DefaultConfig()
			cfg.Raft.DataDir = t.TempDir()
			cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
			cfg.Protocols.REST, cfg.Protocols.GRPC, cfg.Protocols.QUIC = false, false, false
			cfg.Protocols.Unix, cfg.Protocols.MCP = false, false
			tc.set(cfg)
			if n, err := NewStandaloneNode(context.Background(), cfg, nil); n != nil || !tc.want(err) {
				t.Fatalf("node = %v, err = %v", n, err)
			}
		})
		t.Run(name+"/auto-unsealed", func(t *testing.T) {
			cfg := sealedStandaloneConfig(t, t.TempDir())
			tc.set(cfg)
			if n, err := NewStandaloneNode(context.Background(), cfg, nil, WithSealPassphrase([]byte("correct horse"))); n != nil || !tc.want(err) {
				t.Fatalf("node = %v, err = %v", n, err)
			}
		})
	}
}

func TestAttemptAutoUnseal_FailuresKeepTheBarrierSealed(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	passphrase := qrdbconfig.SealConfig{Strategy: string(seal.StrategyPassphrase)}

	t.Run("wrong passphrase", func(t *testing.T) {
		barrier, cleanup := newTestBarrier(t)
		t.Cleanup(cleanup)
		if !attemptAutoUnseal(ctx, barrier, passphrase, []byte("correct horse"), log) {
			t.Fatal("first boot did not initialize and unseal")
		}
		if err := barrier.Seal(); err != nil {
			t.Fatal(err)
		}
		if attemptAutoUnseal(ctx, barrier, passphrase, []byte("battery staple"), log) {
			t.Fatal("a wrong passphrase unsealed the barrier")
		}
		if !barrier.IsSealed() {
			t.Fatal("barrier unsealed after a failed attempt")
		}
	})

	t.Run("already unsealed", func(t *testing.T) {
		barrier, cleanup := newTestBarrier(t)
		t.Cleanup(cleanup)
		if !attemptAutoUnseal(ctx, barrier, passphrase, []byte("correct horse"), log) {
			t.Fatal("first boot did not initialize and unseal")
		}
		if !attemptAutoUnseal(ctx, barrier, passphrase, []byte("correct horse"), log) || barrier.IsSealed() {
			t.Fatal("an attempt on an unsealed barrier must report it unsealed")
		}
	})

	t.Run("unknown strategy cannot initialize without hardware", func(t *testing.T) {
		barrier, cleanup := newTestBarrier(t)
		t.Cleanup(cleanup)
		if attemptAutoUnseal(ctx, barrier, qrdbconfig.SealConfig{Strategy: "hsm-of-the-future"}, nil, log) {
			t.Fatal("an unknown strategy with no credentials unsealed the barrier")
		}
		if !barrier.IsSealed() {
			t.Fatal("barrier unsealed")
		}
	})
}

// An explicitly ephemeral node (no data directory) keeps its seal store in
// memory, defaults the root-key path and credential strategy, and configures
// Shamir from the seal settings -- and leaves no seal directory behind in
// the process's working directory.
func TestBuildNodeSealSubsystem_EphemeralShamirDefaults(t *testing.T) {
	cfg := qrdbconfig.DefaultConfig()
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = string(seal.StrategyShamir)
	cfg.Seal.ShamirThreshold, cfg.Seal.ShamirTotal = 2, 3
	cfg.Seal.RootKeyPath = ""
	cfg.Seal.CredentialStrategy = ""

	sub, err := buildNodeSealSubsystem(context.Background(), cfg, "", slog.New(slog.DiscardHandler), nil, server.CryptoOptions{})
	if err != nil {
		t.Fatalf("buildNodeSealSubsystem: %v", err)
	}
	t.Cleanup(func() { _ = sub.close() })
	if !sub.barrier.IsSealed() {
		t.Error("a freshly built barrier must be sealed")
	}
	if sub.credCoreSvc == nil || sub.barrierRegistry == nil || sub.pinBackend == nil {
		t.Fatalf("subsystem = %+v; want credentials, registry and PIN backend", sub)
	}
	if _, err := os.Stat(sub.sealDataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ephemeral seal store touched %q on disk: %v", sub.sealDataDir, err)
	}
}

// Each construction step's failure is the typed StorageCreateError naming
// the step, with its cause unwrapping.
func TestBuildNodeSealSubsystem_Refusals(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		dataDir string
		set     func(*qrdbconfig.Config)
		backend string
		cause   func(error) bool
	}{
		"seal store under a file": {dataDir: notADir, set: func(*qrdbconfig.Config) {}, backend: "seal",
			cause: func(err error) bool { return err != nil }},
		"unknown strategy": {set: func(c *qrdbconfig.Config) { c.Seal.Strategy = "hsm-of-the-future" }, backend: "seal-strategy",
			cause: func(err error) bool {
				var e *seal.StrategyNotRegisteredError
				return errors.As(err, &e) && e.Name == "hsm-of-the-future"
			}},
		"unknown credential strategy": {set: func(c *qrdbconfig.Config) { c.Seal.CredentialStrategy = "carrier-pigeon" }, backend: "seal-credentials",
			cause: func(err error) bool { return errors.Is(err, credentials.ErrInvalidStrategy) }},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := qrdbconfig.DefaultConfig()
			cfg.Seal.Enabled = true
			cfg.Seal.Strategy = string(seal.StrategyPassphrase)
			tc.set(cfg)
			dataDir := tc.dataDir
			if dataDir == "" {
				dataDir = t.TempDir()
			}
			sub, err := buildNodeSealSubsystem(context.Background(), cfg, dataDir, slog.New(slog.DiscardHandler), nil, server.CryptoOptions{})
			var create *StorageCreateError
			if sub != nil || !errors.As(err, &create) || create.Backend != tc.backend || !tc.cause(create.Err) {
				t.Fatalf("buildNodeSealSubsystem = %v, %v; want *StorageCreateError for %s", sub, err, tc.backend)
			}
		})
	}
}

// stubDecrypter is a keydelivery.Decrypter that is never asked to decrypt.
type stubDecrypter struct{ keydelivery.Decrypter }

func TestNodeOptions_RecordKeyDeliveryAndClusterCA(t *testing.T) {
	var o nodeOptions
	dec := &stubDecrypter{}
	WithKeyDeliveryDecrypter(dec)(&o)
	if o.keyDecrypter != keydelivery.Decrypter(dec) {
		t.Fatalf("keyDecrypter = %v, want the configured decrypter", o.keyDecrypter)
	}
	WithClusterCAMaterial([]byte("ca-cert"), []byte("ca-key"))(&o)
	if string(o.clusterCACertPEM) != "ca-cert" || string(o.clusterCAKeyPEM) != "ca-key" {
		t.Fatalf("cluster CA material = %q, %q", o.clusterCACertPEM, o.clusterCAKeyPEM)
	}
}

// The embedded caller authenticates through the live server, which refuses
// an empty chain; with no server it refuses as recycling.
func TestEmbeddedCaller_Authenticate(t *testing.T) {
	if id, err := (&embeddedCaller{node: &Node{}}).Authenticate(context.Background()); id != nil || !errors.As(err, new(*service.DataPlaneRecyclingError)) {
		t.Fatalf("Authenticate with no server = %v, %v; want *service.DataPlaneRecyclingError", id, err)
	}
	c := &embeddedCaller{node: newBareStandaloneNode(t)}
	if c.Name() != "embedded-caller" {
		t.Errorf("Name = %q", c.Name())
	}
	id, err := c.Authenticate(context.Background())
	if id != nil || !(errors.As(err, new(*server.CallerAuthUnavailableError)) || errors.As(err, new(*server.CallerCertificateError))) {
		t.Fatalf("Authenticate with no chain = %v, %v (%T); want the server's typed refusal", id, err, err)
	}
}

// The volume-pool reporter is not started without a registry, fails to
// construct with no node ID, and fails to start on a sealed node; each
// failure is logged and leaves the node without a reporter.
func TestStartVolumeReporter_Refusals(t *testing.T) {
	ctx := context.Background()
	svcNode := newBareStandaloneNode(t).svcNode.Load()
	registry, err := newVolumeRegistry()
	if err != nil {
		t.Fatal(err)
	}

	(&Node{}).startVolumeReporter(ctx, svcNode, "node-1")

	for name, tc := range map[string]struct {
		nodeID string
		sealed bool
		want   string
	}{
		"no node id":  {nodeID: "", want: "failed to construct volume-pool Reporter"},
		"sealed node": {nodeID: "node-1", sealed: true, want: "failed to start volume-pool Reporter"},
	} {
		t.Run(name, func(t *testing.T) {
			var logs bytes.Buffer
			n := &Node{volumeRegistry: registry, logger: slog.New(slog.NewTextHandler(&logs, nil))}
			if tc.sealed {
				barrier, cleanup := newTestBarrier(t)
				t.Cleanup(cleanup)
				n.barrier.Store(barrier)
			}
			n.startVolumeReporter(ctx, svcNode, tc.nodeID)
			if n.volumeReporter != nil {
				t.Fatal("a refused reporter was recorded on the node")
			}
			if !strings.Contains(logs.String(), tc.want) {
				t.Fatalf("logs = %q; want %q", logs.String(), tc.want)
			}
		})
	}
}

// A node with no barrier registry, volume registry or host attaches no
// catalog and reports no error.
func TestAttachVolumeCatalog_NothingToAttach(t *testing.T) {
	if err := (&Node{}).attachVolumeCatalog(nil); err != nil {
		t.Fatalf("attachVolumeCatalog = %v", err)
	}
}

// With no live server the node's registry refuses a stream as recycling.
func TestNodeServiceRegistry_StreamRefusalWithNoServer(t *testing.T) {
	r := &nodeServiceRegistry{node: &Node{}}
	if err := r.StreamRefusal("acme", "default"); !errors.As(err, new(*service.DataPlaneRecyclingError)) {
		t.Fatalf("StreamRefusal = %v; want *service.DataPlaneRecyclingError", err)
	}
}

// An embedded client over a node with no KV service is the typed
// ClientCreateError, and no client is returned.
func TestNewConnectedEmbeddedClient_RefusesAnEmptyNode(t *testing.T) {
	var create *ClientCreateError
	if c, err := newConnectedEmbeddedClient(context.Background(), &lifecycle.Node{}, nil); c != nil || !errors.As(err, &create) {
		t.Fatalf("client over an empty node = %v, %v; want *ClientCreateError", c, err)
	}
}

// TestNodeFirstBoot_StartedNodeReconciles runs Node.FirstBoot on a started
// node, so the success path reaches reconcileAwaitingAdminCredential. Under
// "enrolled" the node never enters "awaiting admin credential", so no admin
// watch may start.
func TestNodeFirstBoot_StartedNodeReconciles(t *testing.T) {
	n := newBareStandaloneNode(t)
	n.bindingPolicy.Store(int32(auth.MTLSBindingEnrolled))
	users, roles := realStores(t)
	result, err := n.FirstBoot(context.Background(), fbDeps(users, roles))
	if err != nil {
		t.Fatalf("FirstBoot: %v", err)
	}
	if !result.AdminCreated {
		t.Fatal("FirstBoot must create the admin on a fresh store")
	}
	if n.adminWatchStarted.Load() {
		t.Fatal("an enrolled-policy node must not start the admin-binding watch")
	}
}

// TestNodeFirstBoot_InitializeRefusalIsReturned: a first-boot dependency set
// with no tenant service is refused by firstboot.Initialize, and the node
// hands that typed refusal back with no result.
func TestNodeFirstBoot_InitializeRefusalIsReturned(t *testing.T) {
	users, roles := realStores(t)
	deps := fbDeps(users, roles)
	deps.Tenants = nil
	result, err := (&Node{}).FirstBoot(context.Background(), deps)
	var missing *firstboot.MissingTenantsError
	if !errors.As(err, &missing) {
		t.Fatalf("err = %v; want *firstboot.MissingTenantsError", err)
	}
	if result != nil {
		t.Fatalf("result = %+v; want nil on refusal", result)
	}
}

// TestReconcileAwaitingAdminCredential_NoProbeIsANoOp: a node with no
// awaiting-admin-credential probe has no posture to decide, even under
// "required", so no admin watch may start.
func TestReconcileAwaitingAdminCredential_NoProbeIsANoOp(t *testing.T) {
	n := &Node{}
	n.bindingPolicy.Store(int32(auth.MTLSBindingRequired))
	users, _ := realStores(t)
	n.reconcileAwaitingAdminCredential(context.Background(), users, nil)
	if n.adminWatchStarted.Load() {
		t.Fatal("a node with no probe must not start the admin-binding watch")
	}
}

// TestServeSealedWindow_SealedNodeWithNoPendingBoot: a sealed node that holds
// no pending standalone or clustered boot configuration has nothing to serve
// the sealed window from, and is refused with a typed configuration error.
func TestServeSealedWindow_SealedNodeWithNoPendingBoot(t *testing.T) {
	barrier, closeBarrier := newTestBarrier(t)
	defer closeBarrier()
	n := &Node{}
	n.barrier.Store(barrier)
	if !n.IsSealed() {
		t.Fatal("precondition: the node must report sealed")
	}
	var cfgErr *ConfigError
	if err := ServeSealedWindow(context.Background(), n); !errors.As(err, &cfgErr) {
		t.Fatalf("err = %v; want *ConfigError", err)
	}
	if cfgErr.Field != "node" {
		t.Fatalf("Field = %q; want \"node\"", cfgErr.Field)
	}
}

// TestNewVolumeRegistry_RefusesANilProvider: an extra provider the registry
// cannot register fails the build with the registry's own typed error.
func TestNewVolumeRegistry_RefusesANilProvider(t *testing.T) {
	registry, err := newVolumeRegistry(volume.Provider(nil))
	var invalid *volume.InvalidProviderError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v; want *volume.InvalidProviderError", err)
	}
	if registry != nil {
		t.Fatal("a refused build must return no registry")
	}
}

// TestStartClusterSeal_NothingToMonitor: a node with no cluster-seal holder
// starts no monitor and reports success.
func TestStartClusterSeal_NothingToMonitor(t *testing.T) {
	n := &Node{}
	if err := n.startClusterSeal(context.Background(), nil, nil, nil, nil); err != nil {
		t.Fatalf("startClusterSeal: %v", err)
	}
}

// TestStartMemberRecord_NoFeatureClient: with no feature client to publish
// through, no member record is advertised.
func TestStartMemberRecord_NoFeatureClient(t *testing.T) {
	n := &Node{}
	n.startMemberRecord(nil, qrdbconfig.DefaultConfig())
	if err := n.stopMemberRecord(); err != nil {
		t.Fatalf("stopMemberRecord: %v", err)
	}
	if n.memberAdv.Load() != nil {
		t.Fatal("no advertiser may be published without a feature client")
	}
}

// TestNodeFirstBootWithCeremony_InitializeRefusalIsReturned: with no pending
// credential store there is nothing to resolve, and a dependency set with no
// tenant service is still refused by first boot itself.
func TestNodeFirstBootWithCeremony_InitializeRefusalIsReturned(t *testing.T) {
	users, roles := realStores(t)
	deps := fbDeps(users, roles)
	deps.Tenants = nil
	result, err := (&Node{}).FirstBootWithCeremony(context.Background(), nil, nil, 0, deps)
	var missing *firstboot.MissingTenantsError
	if !errors.As(err, &missing) {
		t.Fatalf("err = %v; want *firstboot.MissingTenantsError", err)
	}
	if result != nil {
		t.Fatalf("result = %+v; want nil on refusal", result)
	}
}

// TestReconcileAwaitingAdminCredential_WatchStartsOnce: a "required" node
// whose first boot created the admin unbound enters "awaiting admin
// credential" and starts the admin-binding watch; a second reconcile keeps
// that posture and does not arm the watch again.
func TestReconcileAwaitingAdminCredential_WatchStartsOnce(t *testing.T) {
	n := newBareStandaloneNode(t)
	n.bindingPolicy.Store(int32(auth.MTLSBindingRequired))
	ctx := context.Background()
	svcs := n.Services()
	if _, err := n.FirstBoot(ctx, firstboot.Dependencies{
		Tenants: svcs.Tenant, Users: n.UserService(), Roles: svcs.Role, RoleResolver: svcs.Role,
		Logger: slog.New(slog.DiscardHandler),
	}); err != nil {
		t.Fatalf("FirstBoot: %v", err)
	}
	if !n.awaitingAdminCredentialProbe.AwaitingAdminCredential() || !n.adminWatchStarted.Load() {
		t.Fatal("an unbound admin under \"required\" must leave the node awaiting, with the watch armed")
	}
	n.reconcileAwaitingAdminCredential(ctx, n.UserService(), n.svcNode.Load().Watch)
	if !n.awaitingAdminCredentialProbe.AwaitingAdminCredential() || !n.adminWatchStarted.Load() {
		t.Fatal("a second reconcile must keep the node awaiting with its one watch")
	}
}

// placementWatcherStub reports no placement changes until its context ends.
type placementWatcherStub struct{}

func (placementWatcherStub) Watch(ctx context.Context) (<-chan placement.Event, error) {
	ch := make(chan placement.Event)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

// TestReplicaForwardingOption_ClusteredNodeWithNoLogger: a clustered node
// with forwarding on (the default), a placement watcher and a client gets
// the forwarding server option, logging to the default logger when the node
// has none of its own.
func TestReplicaForwardingOption_ClusteredNodeWithNoLogger(t *testing.T) {
	client := newBareStandaloneNode(t).featureClient.Load()
	if client == nil {
		t.Fatal("precondition: a started node holds a feature client")
	}
	n := &Node{}
	ctx := n.background()
	t.Cleanup(n.bgCancel)
	opt, err := n.replicaForwardingOption(qrdbconfig.DefaultConfig(), false, placementWatcherStub{}, client)
	if err != nil {
		t.Fatalf("replicaForwardingOption: %v", err)
	}
	if opt == nil {
		t.Fatal("a clustered node with forwarding on must get the forwarding option")
	}
	if ctx.Err() != nil {
		t.Fatal("the locator runs on the node's background context, which must still be live")
	}
}

// TestBackgroundWork_NothingToRun: a node with no pending boot, no Raft host
// and no lessor or sweeper has nothing to complete or run, so none of these
// may succeed by starting background work.
func TestBackgroundWork_NothingToRun(t *testing.T) {
	n := &Node{}
	if err := n.completeBoot(context.Background()); err != nil {
		t.Fatalf("completeBoot: %v", err)
	}
	if err := n.startLeaseExpiry(nil, nil, nil, nil, 0, qrdbconfig.LeaseConfig{}); err != nil {
		t.Fatalf("startLeaseExpiry: %v", err)
	}
	n.startReclaimSweep(nil, nil, 0)
	if n.bgCtx != nil {
		t.Fatal("no background context may be created when there is nothing to run")
	}
}
