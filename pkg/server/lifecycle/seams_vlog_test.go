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
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
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

// auditedBarrierTenants is a barrier tenant whose replicated audit is
// entitled, so the opt-in logger hands its events to the replicated writer.
type auditedBarrierTenants struct{ barrierTenants }

func (auditedBarrierTenants) ReplicatedAuditEnabled(context.Context, string) (bool, error) {
	return true, nil
}

// TestBuildServices_AuditAndLogShareTheBarrierTenantsGroup pins that the
// composed node's audit writer and log reader resolve a barrier tenant's
// audit trail through the same resolver: with no home group attached both
// refuse with the same error, and neither falls back to the system shard.
func TestBuildServices_AuditAndLogShareTheBarrierTenantsGroup(t *testing.T) {
	device := &recordingHeadSigners{}
	node := buildSeamNode(t, lifecycle.Infra{HeadSignerProvider: device})
	node.Tenant = auditedBarrierTenants{}
	ctx := tenant.WithTenantID(context.Background(), "vault")

	emitErr := node.ReplicatedAudit.Emit(ctx, audit.AuditEvent{TenantID: "vault", Action: "secret.read"})
	var emitRefused *service.BarrierStoreUnavailableError
	if !errors.As(emitErr, &emitRefused) || emitRefused.TenantID != "vault" {
		t.Fatalf("Emit = %v, want *service.BarrierStoreUnavailableError for vault", emitErr)
	}
	_, headErr := node.Log.Head(ctx, &service.HeadRequest{LogRef: service.LogRef{Database: "system", Log: "audit"}})
	var headRefused *service.BarrierStoreUnavailableError
	if !errors.As(headErr, &headRefused) || headRefused.TenantID != "vault" {
		t.Fatalf("Head = %v, want *service.BarrierStoreUnavailableError for vault", headErr)
	}
	if len(device.created) != 0 {
		t.Fatalf("device asked to create %+v for a refused tenant", device.created)
	}
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
