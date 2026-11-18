// Copyright 2026 Jeremy Hahn
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
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// pendingTTLHost is a clustered daemon host (its groups can hold other
// binaries, as the raft adapter's do) on which pending-user-ttl is activated
// only once active is set. Only the feature query is used.
type pendingTTLHost struct {
	service.Host
	active *atomic.Bool
}

func (h pendingTTLHost) FeatureEnabled(_ uint64, f feature.Feature) bool {
	return f == featuregate.PendingUserTTL && h.active.Load()
}

var _ service.FeatureGatedHost = pendingTTLHost{}

// On the qrdbd daemon path a clustered node refuses a TTL-bearing pending
// create and its reaper sweeps nothing until pending-user-ttl is active;
// after activation both work.
func TestDaemonPendingTTLGate_HoldsTTLCreatesAndTheReaperUntilActive(t *testing.T) {
	var active atomic.Bool
	gate, opt := daemonPendingTTLGate(pendingTTLHost{active: &active})
	if gate == nil {
		t.Fatal("clustered daemon host got no pending-user TTL gate")
	}
	idGen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	svc, err := user.NewStore(kvstore.NewMemoryKVStore(), idGen, opt)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctx := context.Background()
	ttlCreate := func(name string) (*user.User, error) {
		return svc.CreateUser(ctx, &user.CreateUserRequest{
			Username: name, Email: name + "@example.com", TenantID: "acme",
			Pending: true, PendingTTL: time.Minute,
		})
	}

	var notActive *feature.NotActiveError
	if _, err := ttlCreate("early"); !errors.As(err, &notActive) || notActive.Feature != featuregate.PendingUserTTL {
		t.Fatalf("TTL create before activation = %v, want NotActiveError{%s}", err, featuregate.PendingUserTTL)
	}

	leader := gatePendingSweep(gate, nil)
	if leader(ctx, "acme") {
		t.Fatal("reaper leader gate open before activation")
	}
	// An expired account written by a member that activated first stays
	// until this node sees the feature active.
	active.Store(true)
	u, err := ttlCreate("late")
	if err != nil {
		t.Fatalf("TTL create after activation: %v", err)
	}
	active.Store(false)
	clock := func() time.Time { return time.Now().UTC().Add(time.Hour) }
	r, err := user.NewPendingReaper(user.PendingReaperConfig{Users: svc, IsLeader: leader, Now: clock})
	if err != nil {
		t.Fatalf("NewPendingReaper: %v", err)
	}
	if n, err := r.Sweep(ctx, "acme"); err != nil || n != 0 {
		t.Fatalf("sweep before activation = %d, %v; want 0, nil", n, err)
	}
	if got, err := svc.GetUser(ctx, u.ID); err != nil || user.ExpiryClaimed(got) {
		t.Fatalf("account after the held sweep = %+v, %v; want untouched", got, err)
	}

	active.Store(true)
	if !leader(ctx, "acme") {
		t.Fatal("reaper leader gate closed after activation")
	}
	if n, err := r.Sweep(ctx, "acme"); err != nil || n != 1 {
		t.Fatalf("sweep after activation = %d, %v; want 1, nil", n, err)
	}
}

// The reaper gate keeps the per-tenant leader decision once the feature is
// active, and a standalone daemon (no gate) keeps its leader unchanged.
func TestGatePendingSweep_ComposesWithTheLeader(t *testing.T) {
	ctx := context.Background()
	if gatePendingSweep(nil, nil) != nil {
		t.Fatal("standalone (nil gate, nil leader) must keep the sweep-everything leader")
	}
	notLeader := func(context.Context, string) bool { return false }
	if gatePendingSweep(func() error { return nil }, notLeader)(ctx, "acme") {
		t.Fatal("active gate overrode a non-leader")
	}
	if gatePendingSweep(func() error { return errors.New("not yet") }, nil)(ctx, "acme") {
		t.Fatal("inactive gate let a sweep-everything node sweep")
	}
	if gate, _ := daemonPendingTTLGate(nil); gate != nil {
		t.Fatal("a daemon with no host got a pending-user TTL gate")
	}
}
