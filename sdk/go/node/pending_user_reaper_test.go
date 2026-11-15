// Copyright (c) 2026 Jeremy Hahn
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

package node

import (
	"context"
	"errors"
	"testing"
	"time"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

type kvLeader struct {
	id    uint64
	known bool
	err   error
	asked []uint64
}

func (l *kvLeader) LeaderID(shardID uint64) (uint64, bool, error) {
	l.asked = append(l.asked, shardID)
	return l.id, l.known, l.err
}

// A clustered follower never sweeps a system-group tenant: only the KV
// shard's leader deletes its lapsed pending accounts, so each is deleted once
// per pass.
func TestPendingUserLeader_FollowerDoesNotReap(t *testing.T) {
	ctx := context.Background()
	host := &kvLeader{id: 2, known: true}
	isLeader := pendingUserLeader(host, 1, false, nil, nil)
	if isLeader == nil {
		t.Fatal("clustered node got no leader predicate; every replica would sweep")
	}
	if isLeader(ctx, tenantid.System) {
		t.Fatal("follower (leader is replica 2) reported leadership")
	}
	if len(host.asked) != 1 || host.asked[0] != shard.ShardKV {
		t.Fatalf("leadership read from shards %v, want [%d]", host.asked, shard.ShardKV)
	}

	host.id = 1
	if !isLeader(ctx, tenantid.System) {
		t.Fatal("the KV shard's leader did not report leadership")
	}
	for name, h := range map[string]*kvLeader{
		"unknown leader": {id: 1},
		"leader error":   {id: 1, known: true, err: errors.New("closed")},
	} {
		if pendingUserLeader(h, 1, false, nil, nil)(ctx, tenantid.System) {
			t.Fatalf("%s: reported leadership", name)
		}
	}
}

func TestPendingUserLeader_StandaloneSweepsAlways(t *testing.T) {
	if pendingUserLeader(&kvLeader{id: 2, known: true}, 1, true, nil, nil) != nil {
		t.Fatal("standalone node got a leader predicate")
	}
	if pendingUserLeader(nil, 1, false, nil, nil) != nil {
		t.Fatal("node without a Raft host got a leader predicate")
	}
}

// homePlacement is the cluster-wide tenant placement every replica's store
// resolver reports: "iso" lives in its home store, the rest in the system
// group.
type homePlacement map[string]bool

func (p homePlacement) InHome(_ context.Context, tenantID string) (bool, error) {
	return p[tenantID], nil
}

// homeLeads is one replica's view of the tenant home groups it leads.
type homeLeads map[string]bool

func (h homeLeads) LeadsDatabase(tenantID, db string) bool {
	return db == database.TenantHomeName && h[tenantID]
}

// Three replicas: replica 1 leads the KV shard (and holds the isolated
// tenant sealed, so it cannot serve it), replica 2 leads the tenant's home
// group, replica 3 leads neither. Every replica lists every tenant; exactly
// one sweeps each: the KV leader the system and shared tenants, the home
// leader the isolated tenant, which the KV leader alone would never sweep.
func TestPendingUserLeader_ThreeReplicas_IsolatedTenantSweptOnItsHomeLeader(t *testing.T) {
	ctx := context.Background()
	placement := homePlacement{"iso": true}
	kv := &kvLeader{id: 1, known: true}
	gates := map[uint64]func(context.Context, string) bool{}
	for replica, leads := range map[uint64]homeLeads{
		1: {},
		2: {"iso": true},
		3: {},
	} {
		gates[replica] = pendingUserLeader(kv, replica, false, placement, leads)
	}
	for tenantID, want := range map[string]uint64{
		tenantid.System: 1,
		"shared":        1,
		"iso":           2,
	} {
		var sweepers []uint64
		for replica := uint64(1); replica <= 3; replica++ {
			if gates[replica](ctx, tenantID) {
				sweepers = append(sweepers, replica)
			}
		}
		if len(sweepers) != 1 || sweepers[0] != want {
			t.Errorf("tenant %q swept by replicas %v, want exactly [%d]", tenantID, sweepers, want)
		}
	}

	// Home leadership moves to replica 3: the sweep follows it.
	gates[2] = pendingUserLeader(kv, 2, false, placement, homeLeads{})
	gates[3] = pendingUserLeader(kv, 3, false, placement, homeLeads{"iso": true})
	if gates[1](ctx, "iso") || gates[2](ctx, "iso") || !gates[3](ctx, "iso") {
		t.Fatal("the isolated tenant's sweep did not follow its home group's leader")
	}
}

// "off" resolves to a negative interval, which the server reads as no
// reaper; empty is the default and a duration passes through.
func TestPendingUserSweep_ResolvesConfig(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"off":   -1,
		"":      qrdbconfig.DefaultPendingUserSweepInterval,
		"250ms": 250 * time.Millisecond,
		"-5s":   -1,
	} {
		cfg := &qrdbconfig.Config{}
		cfg.Auth.PendingUserSweepInterval = in
		if got := pendingUserSweep(cfg); got != want {
			t.Errorf("pendingUserSweep(%q) = %v, want %v", in, got, want)
		}
		if in == "off" || in == "-5s" {
			if got := pendingUserSweep(cfg); got >= 0 {
				t.Errorf("pendingUserSweep(%q) = %v starts a reaper", in, got)
			}
		}
	}
}
