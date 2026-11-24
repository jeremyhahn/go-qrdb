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

//go:build qrdb_vlog

package lifecycle_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// errDeviceRefused is what the recording provider answers every request with.
var errDeviceRefused = errors.New("device refused")

// recordingHeadSigners is a HeadSignerProvider standing in for a composer's
// hardware one: it records what it is asked and refuses it.
type recordingHeadSigners struct {
	created []vlog.CreateKeyRequest
}

var _ vlog.HeadSignerProvider = (*recordingHeadSigners)(nil)

func (p *recordingHeadSigners) CreateKey(_ context.Context, req vlog.CreateKeyRequest) (vlog.CreatedKey, error) {
	p.created = append(p.created, req)
	return vlog.CreatedKey{}, errDeviceRefused
}

func (*recordingHeadSigners) HeadSigner(context.Context, vlog.HeadSignerRequest) (vlog.HeadSigner, error) {
	return nil, errDeviceRefused
}

// TestBuildServices_HeadSignerProviderCreatesTheLogsKeys: an injected
// provider is the one the log service asks for a new log's key.
func TestBuildServices_HeadSignerProviderCreatesTheLogsKeys(t *testing.T) {
	device := &recordingHeadSigners{}
	node := buildSeamNode(t, lifecycle.Infra{HeadSignerProvider: device})
	if node.Log == nil {
		t.Fatal("Log is nil in a build with the verifiable log compiled in")
	}
	ctx := tenant.WithTenantID(context.Background(), "acme")
	_, err := node.Log.CreateLog(ctx, &service.CreateLogRequest{
		LogRef: service.LogRef{Database: "ledger", Log: "events"}, Origin: "db.example.com/events", SigningKeyType: vlog.SignatureEd25519,
	})
	if !errors.Is(err, errDeviceRefused) {
		t.Fatalf("CreateLog = %v, want the device's refusal", err)
	}
	if len(device.created) != 1 || device.created[0].TenantID != "acme" || device.created[0].Version != vlog.FirstKeyVersion {
		t.Fatalf("device asked to create %+v", device.created)
	}
}

// TestBuildServices_HeadSignerProviderAbsentKeepsTheSoftwareProvider: with no
// provider the log service is still composed, over the software default.
func TestBuildServices_HeadSignerProviderAbsentKeepsTheSoftwareProvider(t *testing.T) {
	node := buildSeamNode(t, lifecycle.Infra{})
	if node.Log == nil {
		t.Fatal("Log is nil with no provider injected; want the software default")
	}
}

// barrierTenants answers every tenant as barrier-isolated; the node's
// isolation lookup calls nothing else.
type barrierTenants struct {
	tenant.TenantServicer
}

func (barrierTenants) GetTenant(_ context.Context, id string) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: id, IsolationType: tenant.IsolationBarrier}, nil
}

// TestBuildServices_LogServiceRoutesBarrierTenantsToTheirHome: a barrier
// tenant's log and signing key are applied in the tenant's own home group,
// never the system shard; before the node attaches its homes the composed log
// service fails closed before any key is created.
func TestBuildServices_LogServiceRoutesBarrierTenantsToTheirHome(t *testing.T) {
	device := &recordingHeadSigners{}
	node := buildSeamNode(t, lifecycle.Infra{HeadSignerProvider: device})
	node.Tenant = barrierTenants{}
	ctx := tenant.WithTenantID(context.Background(), "vault")
	_, err := node.Log.CreateLog(ctx, &service.CreateLogRequest{
		LogRef: service.LogRef{Database: "ledger", Log: "events"}, Origin: "db.example.com/events", SigningKeyType: vlog.SignatureEd25519,
	})
	var refused *service.BarrierStoreUnavailableError
	if !errors.As(err, &refused) || refused.TenantID != "vault" {
		t.Fatalf("CreateLog = %v, want *service.BarrierStoreUnavailableError for vault", err)
	}
	if len(device.created) != 0 {
		t.Fatalf("device asked to create %+v for a refused tenant", device.created)
	}
}

// TestBuildServices_StandaloneBarrierTenantsAuditIsWrittenToTheSystemShard:
// on a composed standalone node whose barrier tenant's home is attached by
// AttachLocalTenantHomes, the tenant's audit events are written to the
// system shard, as on main, and read back from it; the log service routes
// the audit instance to the same shard. A standalone node gates no feature,
// so it holds no verifiable audit instance for any tenant (OpAuditAppend);
// the provable trail is a raft node's (TestLogService_ReadsABarrierTenants
// AuditInstanceWhereItsLoggerWrites in pkg/service).
func TestBuildServices_StandaloneBarrierTenantsAuditIsWrittenToTheSystemShard(t *testing.T) {
	mvcc, err := engine.NewMVCCEngine(memory.NewStorage(), engine.WithMVCCLogs(vlog.NewChain()))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	// A standalone node's system shard is a local backend that gates no
	// features (cmd/qrdbd): every replica is this process, so its
	// replicated audit is the tenant's audit instance.
	host := backend.NewLocalBackend(mvcc)
	t.Cleanup(func() { _ = host.Close() })
	node, registry := buildRoutedIAMNodeOn(t, host)
	ctx := context.Background()
	if _, err := node.Tenant.CreateTenant(ctx, "vault", "Vault", tenant.IsolationBarrier); err != nil {
		t.Fatalf("CreateTenant(barrier): %v", err)
	}
	if err := registry.InitializeTenant(ctx, "vault", seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant(barrier): %v", err)
	}
	node.Tenant = auditedTenants{TenantServicer: node.Tenant}
	vault := tenant.WithTenantID(ctx, "vault")

	for _, action := range []string{"secret.read", "secret.write"} {
		if err := node.ReplicatedAudit.Emit(vault, audit.AuditEvent{TenantID: "vault", Action: action}); err != nil {
			t.Fatalf("Emit(%s): %v", action, err)
		}
	}
	page, err := node.ReplicatedAudit.Query(vault, audit.Filter{TenantID: "vault"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	seen := map[string]bool{}
	for _, ev := range page.Events {
		seen[ev.Action] = true
	}
	if !seen["secret.read"] || !seen["secret.write"] {
		t.Fatalf("Query = %+v, want both audited events read back from the system shard", page.Events)
	}
	// The log service routes the audit instance where the logger writes:
	// the system shard, never the tenant's home store.
	head, headErr := node.Log.Head(vault, &service.HeadRequest{LogRef: service.LogRef{Database: vlog.AuditDatabase, Log: vlog.AuditLog}})
	var unavailable *service.BarrierStoreUnavailableError
	if errors.As(headErr, &unavailable) || errors.Is(headErr, tenant.ErrTenantSealed) {
		t.Fatalf("Head(audit) = %v, want it routed to the system shard, not the barrier home", headErr)
	}
	// A single-binary host writes the trail as the tenant's audit instance:
	// both events are leaves of the system shard's vault/system/audit.
	if headErr != nil || head.Head.Size < 2 {
		t.Fatalf("Head(audit) = %+v, %v; want the audit instance holding both events", head, headErr)
	}
}

// TestBuildServices_StandaloneAuditSwitchKeepsTheChainContinuous pins the
// switch on a standalone node whose trail predates it: the system shard (a
// local backend) holds events committed as OpAuditAppend, the replicated
// logger then writes the tenant's audit instance, and Query reads every
// event while VerifyTenantChain folds the pre-switch records and the
// instance's leaves into one continuous chain from genesis (leaf 0 carries
// the pre-switch PrevHash, docs/databases/engineering.md "The switch").
func TestBuildServices_StandaloneAuditSwitchKeepsTheChainContinuous(t *testing.T) {
	mvcc, err := engine.NewMVCCEngine(memory.NewStorage(), engine.WithMVCCLogs(vlog.NewChain()))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	host := backend.NewLocalBackend(mvcc)
	t.Cleanup(func() { _ = host.Close() })
	node, registry := buildRoutedIAMNodeOn(t, host)
	ctx := context.Background()
	if _, err := node.Tenant.CreateTenant(ctx, "vault", "Vault", tenant.IsolationBarrier); err != nil {
		t.Fatalf("CreateTenant(barrier): %v", err)
	}
	if err := registry.InitializeTenant(ctx, "vault", seal.Credentials{}); err != nil {
		t.Fatalf("InitializeTenant(barrier): %v", err)
	}
	node.Tenant = auditedTenants{TenantServicer: node.Tenant}
	vault := tenant.WithTenantID(ctx, "vault")
	auditRef := service.LogRef{Database: vlog.AuditDatabase, Log: vlog.AuditLog}

	// The trail an earlier binary wrote: OpAuditAppend, no tree node.
	for _, action := range []string{"pre.one", "pre.two"} {
		cmd, err := protocol.EncodeAuditAppend("vault", audit.AuditEvent{TenantID: "vault", Action: action, Timestamp: time.Now().UTC()})
		if err != nil {
			t.Fatalf("EncodeAuditAppend(%s): %v", action, err)
		}
		if _, err := host.SyncPropose(ctx, 0, cmd); err != nil {
			t.Fatalf("SyncPropose(%s): %v", action, err)
		}
	}
	if _, err := node.Log.Head(vault, &service.HeadRequest{LogRef: auditRef}); err == nil {
		t.Fatal("the pre-switch trail already has an audit instance")
	}
	for _, action := range []string{"post.one", "post.two"} {
		if err := node.ReplicatedAudit.Emit(vault, audit.AuditEvent{TenantID: "vault", Action: action}); err != nil {
			t.Fatalf("Emit(%s): %v", action, err)
		}
	}

	head, err := node.Log.Head(vault, &service.HeadRequest{LogRef: auditRef})
	if err != nil || head.Head.Size != 2 {
		t.Fatalf("Head(audit) = %+v, %v; want the two post-switch leaves", head, err)
	}
	page, err := node.ReplicatedAudit.Query(vault, audit.Filter{TenantID: "vault"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	seen := map[string]bool{}
	for _, ev := range page.Events {
		seen[ev.Action] = true
	}
	for _, action := range []string{"pre.one", "pre.two", "post.one", "post.two"} {
		if !seen[action] {
			t.Fatalf("Query = %+v, want %s read back across the switch", page.Events, action)
		}
	}
	report, err := node.ReplicatedAudit.VerifyTenantChain(vault, "vault")
	if err != nil || !report.Verified || !report.Genesis || report.Records < 4 {
		t.Fatalf("VerifyTenantChain = %+v, %v; want one continuous chain from genesis across the switch", report, err)
	}
}

// auditedTenants is a composed node's tenant service with replicated audit
// entitled for every tenant.
type auditedTenants struct{ tenant.TenantServicer }

func (auditedTenants) ReplicatedAuditEnabled(context.Context, string) (bool, error) {
	return true, nil
}

// TestBuildServices_LogServiceServesBarrierDatabases: the composed log
// service checks the node's own database registry and serves a log in a
// barrier-enabled database from that database's own barrier store.
func TestBuildServices_LogServiceServesBarrierDatabases(t *testing.T) {
	barrier, registry, credSvc := newTestBarrier(t)
	dbs := databasetest.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = dbs.Close() })
	device := &recordingHeadSigners{}
	node := buildSeamNode(t, lifecycle.Infra{
		SealBarrier:        barrier,
		BarrierRegistry:    registry,
		CredentialCoreSvc:  credSvc,
		DatabaseManager:    dbs,
		HeadSignerProvider: device,
	})
	ctx := context.Background()
	if _, err := dbs.CreateWithCredentials(ctx, database.DatabaseConfig{
		TenantID: "acme", Name: "secure", Backend: database.BackendMemory, DataDir: t.TempDir(),
		Barrier: &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase},
	}, seal.MustCredentials(ctx, []byte("db-pass"))); err != nil {
		t.Fatalf("create the barrier database: %v", err)
	}
	_, err := node.Log.CreateLog(tenant.WithTenantID(ctx, "acme"), &service.CreateLogRequest{
		LogRef: service.LogRef{Database: "secure", Log: "events"}, Origin: "db.example.com/events", SigningKeyType: vlog.SignatureEd25519,
	})
	// The double refuses every key: reaching it shows the barrier database
	// was routed to its store rather than refused before the device.
	if !errors.Is(err, errDeviceRefused) {
		t.Fatalf("CreateLog in acme's barrier database secure = %v, want the device's refusal", err)
	}
}

func TestBuildServices_LogServiceSealsWithTheBarrierDatabase(t *testing.T) {
	barrier, registry, credSvc := newTestBarrier(t)
	dbs := databasetest.NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = dbs.Close() })
	// The system shard host is built here rather than by buildSeamNode so
	// the test can read what was written to it.
	stg := memory.NewStorage()
	mvcc, err := engine.NewMVCCEngine(stg, engine.WithMVCCLogs(vlog.NewChain()))
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	host := backend.NewLocalBackend(mvcc)
	t.Cleanup(func() { _ = host.Close() })
	hub := watch.NewHub()
	t.Cleanup(hub.Close)
	node, err := lifecycle.BuildServices(context.Background(), lifecycle.Infra{
		Host:              host,
		WatchHub:          hub,
		DataDir:           t.TempDir(),
		SealBarrier:       barrier,
		BarrierRegistry:   registry,
		CredentialCoreSvc: credSvc,
		DatabaseManager:   dbs,
	})
	if err != nil {
		t.Fatalf("BuildServices: %v", err)
	}
	t.Cleanup(func() { _ = node.Stop(context.Background()) })
	ctx := context.Background()
	if _, err := dbs.CreateWithCredentials(ctx, database.DatabaseConfig{
		TenantID: "acme", Name: "secure", Backend: database.BackendMemory, DataDir: t.TempDir(),
		Barrier: &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModePassphrase},
	}, seal.MustCredentials(ctx, []byte("db-pass"))); err != nil {
		t.Fatalf("create the barrier database: %v", err)
	}
	acme := tenant.WithTenantID(ctx, "acme")
	ref := service.LogRef{Database: "secure", Log: "events"}
	const plaintext = "barrier-plaintext-entry"
	if _, err := node.Log.CreateLog(acme, &service.CreateLogRequest{LogRef: ref, Origin: "db.example.com/events", SigningKeyType: vlog.SignatureEd25519}); err != nil {
		t.Fatalf("CreateLog: %v", err)
	}
	if _, err := node.Log.Append(acme, &service.AppendRequest{LogRef: ref, Entries: [][]byte{[]byte(plaintext)}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if head, err := node.Log.Head(acme, &service.HeadRequest{LogRef: ref}); err != nil || head.Head.Size != 1 {
		t.Fatalf("Head = %+v, %v; want size 1", head, err)
	}

	bm := dbs.BarrierManager()
	if bm == nil {
		t.Fatalf("the composed node left the database manager without a barrier manager")
	}
	if err := bm.SealDatabase("secure", "acme"); err != nil {
		t.Fatalf("SealDatabase: %v", err)
	}
	second := service.LogRef{Database: "secure", Log: "second"}
	var sealed *service.BarrierSealedError
	if _, err := node.Log.CreateLog(acme, &service.CreateLogRequest{LogRef: second, Origin: "db.example.com/second", SigningKeyType: vlog.SignatureEd25519}); !errors.As(err, &sealed) || sealed.TenantID != "acme" || sealed.Database != "secure" {
		t.Fatalf("CreateLog in the sealed database = %v, want *service.BarrierSealedError for acme/secure", err)
	}
	if _, err := node.Log.Head(acme, &service.HeadRequest{LogRef: ref}); !errors.As(err, &sealed) || sealed.TenantID != "acme" || sealed.Database != "secure" {
		t.Fatalf("Head in the sealed database = %v, want *service.BarrierSealedError for acme/secure", err)
	}

	it := stg.NewIterator(&storage.IteratorOptions{})
	defer func() { _ = it.Close() }()
	records := 0
	for ok := it.SeekToFirst(); ok; ok = it.Next() {
		// The barrier database's logs live in the system shard's group,
		// each body sealed under the database's key before it is proposed.
		if bytes.Contains(it.Key(), []byte("qrdb.vlog/acme/secure")) {
			records++
		}
		if bytes.Contains(it.Value(), []byte(plaintext)) {
			t.Errorf("the system shard host holds acme's appended entry in plaintext under %q", it.Key())
		}
	}
	if err := it.Error(); err != nil {
		t.Fatalf("scan the system shard host: %v", err)
	}
	if records == 0 {
		t.Fatalf("the system shard host holds no log record of acme's barrier database")
	}
}
