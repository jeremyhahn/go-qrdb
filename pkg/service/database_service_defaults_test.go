package service

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// newDefaultsDatabaseService returns a tenant-scoped database service whose
// manager carries the node database_defaults d.
func newDefaultsDatabaseService(t *testing.T, d database.Defaults, info *tenant.TenantInfo, ent *mockEntitlementChecker, f *fakeReplicaQuota) DatabaseServicer {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := databasetest.NewManager(logger, t.TempDir(), database.WithDefaults(d))
	mgr.SetShardIDClaimer(testShardClaimer(t))
	t.Cleanup(func() { _ = mgr.Close() })
	return NewDatabaseService(mgr,
		WithEntitlementChecker(ent),
		WithTenantInfoLookup(&fakeTenantInfoLookup{info: info}),
		WithQuotaChecker(newReplicaQuotaChecker(t, f)),
		WithQuotaUsageRecorder(f),
	)
}

// TestCreateForTenant_Defaults_ReplicasBeforeNormalisation pins that a raft
// create omitting its replicas records and meters database_defaults.replicas,
// not the single replica the service substitutes for an unset count.
func TestCreateForTenant_Defaults_ReplicasBeforeNormalisation(t *testing.T) {
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 5, MaxTotalReplicas: 10}}
	svc := newDefaultsDatabaseService(t, database.Defaults{Replicas: 3}, raftTenant(t), &mockEntitlementChecker{}, f)
	ctx := tenant.WithTenantID(context.Background(), "rt")

	got, err := svc.CreateForTenant(ctx, database.DatabaseConfig{Name: "rf", Backend: database.BackendRaft})
	if err != nil {
		t.Fatalf("CreateForTenant: %v", err)
	}
	if got.Replicas != 3 {
		t.Errorf("persisted Replicas = %d, want 3 (database_defaults.replicas)", got.Replicas)
	}
	if f.incTotal != 3 {
		t.Errorf("total-replica increment = %d, want 3", f.incTotal)
	}
}

// TestCreateForTenant_Defaults_RaftBackendEntitled pins that a create
// omitting its backend under a node raft default passes the raft
// entitlement check: a plan without the raft tier refuses it.
func TestCreateForTenant_Defaults_RaftBackendEntitled(t *testing.T) {
	errNoRaft := errors.New("plan has no raft tier")
	ent := &mockEntitlementChecker{isolationFn: func(_ context.Context, _, mode string) error {
		if mode == quota.IsolationModeRaft {
			return errNoRaft
		}
		return nil
	}}
	f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 3, MaxTotalReplicas: 10}}
	d := database.Defaults{Backend: database.BackendRaft, RaftClusterMode: database.RaftLocal}
	prefix := &tenant.TenantInfo{ID: "acme", IsolationType: tenant.IsolationPrefix}
	svc := newDefaultsDatabaseService(t, d, prefix, ent, f)
	ctx := tenant.WithTenantID(context.Background(), "acme")

	if _, err := svc.CreateForTenant(ctx, database.DatabaseConfig{Name: "dflt"}); !errors.Is(err, errNoRaft) {
		t.Fatalf("defaulted raft create without the raft tier = %v, want the entitlement refusal", err)
	}
	if f.incTotal != 0 {
		t.Fatalf("a refused create reserved %d replicas", f.incTotal)
	}
}

// newSystemDefaultsDatabaseService returns a system-scope database service
// whose manager carries the node database_defaults d.
func newSystemDefaultsDatabaseService(t *testing.T, d database.Defaults) DatabaseServicer {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mgr := databasetest.NewManager(logger, t.TempDir(), database.WithDefaults(d))
	mgr.SetShardIDClaimer(testShardClaimer(t))
	t.Cleanup(func() { _ = mgr.Close() })
	return NewDatabaseService(mgr)
}

// TestCreate_Defaults_RaftTopologyResolvedFirst pins that the raft topology
// is validated on the config the manager records, on both the tenant and the
// system create path: a defaulted distributed cluster mode needs placement,
// an omitted backend under a raft default still meets the explicit cluster
// mode none refusal, and the cluster mode none the defaults fall back to when
// nothing names one passes.
func TestCreate_Defaults_RaftTopologyResolvedFirst(t *testing.T) {
	raftDefault := database.Defaults{Backend: database.BackendRaft}
	distributed := database.Defaults{Backend: database.BackendRaft, RaftClusterMode: database.RaftDistributed}
	prefix := &tenant.TenantInfo{ID: "acme", IsolationType: tenant.IsolationPrefix}
	paths := []struct {
		name   string
		create func(t *testing.T, d database.Defaults, cfg database.DatabaseConfig) (*database.DatabaseInfo, error)
	}{
		{"tenant", func(t *testing.T, d database.Defaults, cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
			f := &fakeReplicaQuota{q: &quota.Quotas{MaxReplicasPerGroup: 3, MaxTotalReplicas: 10}}
			svc := newDefaultsDatabaseService(t, d, prefix, &mockEntitlementChecker{}, f)
			return svc.CreateForTenant(tenant.WithTenantID(context.Background(), "acme"), cfg)
		}},
		{"system", func(t *testing.T, d database.Defaults, cfg database.DatabaseConfig) (*database.DatabaseInfo, error) {
			return newSystemDefaultsDatabaseService(t, d).Create(cfg)
		}},
	}
	for _, p := range paths {
		t.Run(p.name, func(t *testing.T) {
			_, err := p.create(t, distributed, database.DatabaseConfig{Name: "dist"})
			var rdu *RaftDistributedUnavailableError
			if !errors.As(err, &rdu) {
				t.Fatalf("defaulted distributed create without placement = %v, want *RaftDistributedUnavailableError", err)
			}

			_, err = p.create(t, raftDefault, database.DatabaseConfig{
				Name: "none", Raft: &database.DatabaseRaftConfig{ClusterMode: database.RaftNone},
			})
			var irt *InvalidRaftTopologyError
			if !errors.As(err, &irt) {
				t.Fatalf("defaulted raft backend naming cluster mode none = %v, want *InvalidRaftTopologyError", err)
			}

			info, err := p.create(t, raftDefault, database.DatabaseConfig{Name: "fallback"})
			if err != nil {
				t.Fatalf("defaulted raft create with the fallback block = %v", err)
			}
			if info.Raft == nil || info.Raft.ClusterMode != database.RaftNone {
				t.Fatalf("recorded raft = %+v, want the cluster mode none fallback", info.Raft)
			}
		})
	}
}
