package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// prefixTenantService reports every tenant as prefix-isolated.
type prefixTenantService struct {
	tenant.TenantServicer
}

func (prefixTenantService) GetTenant(_ context.Context, id string) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: id, IsolationType: tenant.IsolationPrefix}, nil
}

// TestInstallBindingFactory_RaftModeAndStandalone proves both start paths
// install the node's binding factory on the database manager, and that the
// raft-mode one leaves a backend: raft database to the Manager's raft path
// (described as a raft binding) where the standalone one refuses it, while a
// pebble database is local on both.
func TestInstallBindingFactory_RaftModeAndStandalone(t *testing.T) {
	tenants := func() tenant.TenantServicer { return prefixTenantService{} }
	barriers := func() *seal.BarrierRegistry { return nil }
	for _, raftMode := range []bool{true, false} {
		dir := t.TempDir()
		m := database.NewManager(slog.Default(), dir)
		bf, err := installBindingFactory(m, dir, database.Defaults{}, nil, qrdbconfig.StorageConfig{},
			tenants, barriers, raftMode)
		if err != nil || bf == nil {
			t.Fatalf("installBindingFactory(raftMode=%v) = %v, %v; want a factory", raftMode, bf, err)
		}
		pebble, err := bf.DescribeBinding(database.DatabaseConfig{TenantID: "acme", Name: "orders", Backend: database.BackendPebble})
		if err != nil || pebble == nil || pebble.Kind != database.BindingLocal {
			t.Fatalf("raftMode=%v: DescribeBinding(pebble) = %+v, %v; want a local binding", raftMode, pebble, err)
		}
		raft, err := bf.DescribeBinding(database.DatabaseConfig{TenantID: "acme", Name: "ledger", Backend: database.BackendRaft})
		var unavailable *database.RaftBackendUnavailableError
		if raftMode {
			if err != nil || raft == nil || raft.Kind != database.BindingRaft {
				t.Fatalf("raft mode: DescribeBinding(raft) = %+v, %v; want a raft binding", raft, err)
			}
		} else if !errors.As(err, &unavailable) {
			t.Fatalf("standalone: DescribeBinding(raft) err = %v, want *RaftBackendUnavailableError", err)
		}
		// A second install on the same manager replaces the factory
		// while no database is open.
		if _, err := installBindingFactory(m, dir, database.Defaults{}, nil, qrdbconfig.StorageConfig{},
			tenants, barriers, raftMode); err != nil {
			t.Fatalf("second installBindingFactory(raftMode=%v): %v", raftMode, err)
		}
		_ = m.Close()
	}
}
