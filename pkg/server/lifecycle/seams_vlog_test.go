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

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
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

// TestBuildServices_LogServiceRoutesBarrierTenantsToTheirHomeGroup: without a
// home group a barrier tenant's log and signing key would be applied under
// the system barrier, so the composed log service refuses the tenant before
// any key is created; once the tenant homes are attached it routes the
// tenant to its home group.
func TestBuildServices_LogServiceRoutesBarrierTenantsToTheirHomeGroup(t *testing.T) {
	device := &recordingHeadSigners{}
	node := buildSeamNode(t, lifecycle.Infra{HeadSignerProvider: device})
	node.Tenant = barrierTenants{}
	ctx := tenant.WithTenantID(context.Background(), "vault")
	createLog := func() error {
		_, err := node.Log.CreateLog(ctx, &service.CreateLogRequest{
			LogRef: service.LogRef{Database: "ledger", Log: "events"}, Origin: "db.example.com/events", SigningKeyType: vlog.SignatureEd25519,
		})
		return err
	}

	t.Run("refused without a home group", func(t *testing.T) {
		var refused *service.BarrierTenantLogNotSupportedError
		if err := createLog(); !errors.As(err, &refused) || refused.TenantID != "vault" {
			t.Fatalf("CreateLog = %v, want *service.BarrierTenantLogNotSupportedError for vault", err)
		}
		if len(device.created) != 0 {
			t.Fatalf("device asked to create %+v for a refused tenant", device.created)
		}
	})

	t.Run("routed with one", func(t *testing.T) {
		homes := &askedHomeGroups{}
		if !lifecycle.AttachTenantHomes(node, homes) {
			t.Fatal("AttachTenantHomes = false, want true")
		}
		if err := createLog(); !errors.Is(err, errHomeGroupAsked) {
			t.Fatalf("CreateLog = %v, want the home group's answer", err)
		}
		if len(homes.asked) != 1 || homes.asked[0] != "vault" {
			t.Fatalf("home groups asked for %v, want [vault]", homes.asked)
		}
	})
}

// errHomeGroupAsked is what askedHomeGroups answers every home group with.
var errHomeGroupAsked = errors.New("home group asked")

// askedHomeGroups is a raft node's tenant homes: it records which tenants'
// home groups are asked for and answers each with errHomeGroupAsked.
type askedHomeGroups struct {
	asked []string
}

func (*askedHomeGroups) Services(context.Context, string, string) (*server.DatabaseServices, error) {
	return nil, errHomeGroupAsked
}

func (*askedHomeGroups) IsShardJoining(uint64) bool { return false }

func (h *askedHomeGroups) HomeGroup(_ context.Context, tenantID string) (service.Host, uint64, error) {
	h.asked = append(h.asked, tenantID)
	return nil, 0, errHomeGroupAsked
}

// TestBuildServices_LogServiceRefusesBarrierDatabases: a barrier database's
// log and signing key would be applied under the system barrier, not the
// database's, so the composed log service checks the node's own database
// registry and refuses the database before any key is created.
func TestBuildServices_LogServiceRefusesBarrierDatabases(t *testing.T) {
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
	var refused *service.BarrierDatabaseLogNotSupportedError
	if !errors.As(err, &refused) || refused.TenantID != "acme" || refused.Database != "secure" {
		t.Fatalf("CreateLog = %v, want *service.BarrierDatabaseLogNotSupportedError for acme's secure", err)
	}
	if len(device.created) != 0 {
		t.Fatalf("device asked to create %+v for a refused database", device.created)
	}
}
