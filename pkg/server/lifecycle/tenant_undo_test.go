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

package lifecycle

import (
	"context"
	"errors"
	"slices"
	"testing"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// membershipHost answers the system shard's committed membership.
type membershipHost struct {
	service.AdminHost
	shardID uint64
	m       *quicraft.Membership
	err     error
}

var _ service.AdminHost = (*membershipHost)(nil)

func (h *membershipHost) SyncGetShardMembership(_ context.Context, shardID uint64) (*quicraft.Membership, error) {
	h.shardID = shardID
	return h.m, h.err
}

func TestTenantUndoRequired_ActiveOnAStandaloneNode(t *testing.T) {
	required := tenantUndoRequired(newStandaloneHost())
	if required == nil {
		t.Fatal("tenantUndoRequired(standalone) = nil, want an always-active check")
	}
	if err := required(); err != nil {
		t.Fatalf("required() on a standalone node = %v, want nil", err)
	}
}

func TestTenantUndoRequired_NilWhenNothingGates(t *testing.T) {
	for name, host := range map[string]service.Host{"no host": nil, "ungated": &membershipHost{}} {
		t.Run(name, func(t *testing.T) {
			if required := tenantUndoRequired(host); required != nil {
				t.Fatalf("tenantUndoRequired = %v, want nil: the undo stays a retirement", required())
			}
		})
	}
}

func TestTenantUndoRequired_FollowsTheGate(t *testing.T) {
	// Another feature being active does not stand in for this one.
	before := gatedHost{enabled: map[feature.Feature]bool{featuregate.PerNodeBarrierKeys: true}}
	var notActive *feature.NotActiveError
	if err := tenantUndoRequired(before)(); !errors.As(err, &notActive) || notActive.Feature != featuregate.TenantUndo || notActive.ShardID != 0 {
		t.Fatalf("required before activation = %v, want cluster-wide NotActiveError(%s)", err, featuregate.TenantUndo)
	}
	after := gatedHost{enabled: map[feature.Feature]bool{featuregate.TenantUndo: true}}
	if err := tenantUndoRequired(after)(); err != nil {
		t.Fatalf("required after activation = %v, want nil", err)
	}
}

func TestUndoCleanupRoster_StandaloneListsItself(t *testing.T) {
	roster, err := undoCleanupRoster(newStandaloneHost())(context.Background())
	if err != nil || !slices.Equal(roster, []string{LocalNodeID}) {
		t.Fatalf("roster = %v, %v; want [%s]", roster, err, LocalNodeID)
	}
}

func TestUndoCleanupRoster_EveryMemberOfTheSystemShard(t *testing.T) {
	host := &membershipHost{m: &quicraft.Membership{
		Nodes:     map[uint64]string{1: "n2:7000", 2: "n1:7000"},
		Observers: map[uint64]string{3: "n3:7000"},
		Witnesses: map[uint64]string{4: "n4:7000", 5: "n1:7000"},
	}}
	roster, err := undoCleanupRoster(host)(context.Background())
	if err != nil {
		t.Fatalf("roster: %v", err)
	}
	if want := []string{"n1:7000", "n2:7000", "n3:7000", "n4:7000"}; !slices.Equal(roster, want) {
		t.Fatalf("roster = %v, want %v", roster, want)
	}
	if host.shardID != shard.ShardKV {
		t.Fatalf("membership read of shard %d, want the system shard %d", host.shardID, shard.ShardKV)
	}
}

func TestUndoCleanupRoster_MembershipErrorAndNoHost(t *testing.T) {
	boom := errors.New("no quorum")
	if _, err := undoCleanupRoster(&membershipHost{err: boom})(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("roster error = %v, want %v", err, boom)
	}
	if roster := undoCleanupRoster(nil); roster != nil {
		t.Fatal("undoCleanupRoster(nil) = non-nil, want nil")
	}
}
