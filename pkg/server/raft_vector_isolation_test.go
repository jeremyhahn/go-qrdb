//go:build qrdb_vector

// A tenant-scoped namespace create builds a real index, which without the
// tag fails "vector support not compiled"; the Makefile's unit target
// composes qrdb_vector.

package server

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// raftVectorFactory builds a raft-mode factory whose prefix tenant "pfx"
// holds the backend: raft database "app", with the given options.
func raftVectorFactory(t *testing.T, opts ...RaftBackendOption) *RaftBackendFactory {
	t.Helper()
	host, mux := newSharedTestHostWithMux(t)
	reg := newVolumePooledRegistry(t)
	src := newFakeRaftInfoSource()
	src.add("pfx", "app", 1102, t.TempDir())
	lookup := func(context.Context, string) (tenant.IsolationType, error) { return tenant.IsolationPrefix, nil }
	opts = append([]RaftBackendOption{WithMultiplexLogDB(mux), WithTenantIsolation(lookup)}, opts...)
	f := NewRaftBackendFactory(host, sharedHostReplicaID, sharedHostRaftAddr, reg, src, volTestLogger(), opts...)
	t.Cleanup(func() { _ = f.CloseAll() })
	return f
}

// A raft-backed database's vector service enforces the live strict
// isolation policy: a system-tenant namespace create is refused with
// *service.TenantRequiredError and a tenant-scoped create succeeds.
func TestRaftBackendFactory_VectorEnforcesStrictIsolation(t *testing.T) {
	f := raftVectorFactory(t, WithClusterIsolationPolicy(fixedIsolationPolicy{policy: tenant.ClusterIsolationPolicyStrict}))
	svcs, err := f.Services(context.Background(), "pfx", "app")
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if svcs.Vector == nil {
		t.Fatal("raft database bundle has no vector service")
	}
	req := func(ns string) *service.CreateNamespaceRequest {
		return &service.CreateNamespaceRequest{Namespace: ns, Dimension: 2, DistanceMetric: "L2"}
	}
	_, err = svcs.Vector.CreateNamespace(context.Background(), req("sysns"))
	var required *service.TenantRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("system-tenant CreateNamespace = %v; want *service.TenantRequiredError", err)
	}
	if _, err := svcs.Vector.CreateNamespace(tenant.WithTenantID(context.Background(), "pfx"), req("tns")); err != nil {
		t.Fatalf("tenant-scoped CreateNamespace: %v", err)
	}
}

// A factory without the isolation policy reader builds no vector service
// rather than one that skips the strict-isolation check.
func TestRaftShardVector_RefusesWithoutIsolationPolicy(t *testing.T) {
	if _, _, err := newRaftShardVector(nil, 1, nil); !errors.Is(err, errRaftVectorNoIsolationPolicy) {
		t.Fatalf("newRaftShardVector without a policy = %v; want errRaftVectorNoIsolationPolicy", err)
	}
}
