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
	"errors"
	"testing"
	"time"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
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

// A clustered follower never sweeps: only the KV shard's leader deletes
// lapsed pending accounts, so each is deleted once per pass.
func TestPendingUserLeader_FollowerDoesNotReap(t *testing.T) {
	host := &kvLeader{id: 2, known: true}
	isLeader := pendingUserLeader(host, 1, false)
	if isLeader == nil {
		t.Fatal("clustered node got no leader predicate; every replica would sweep")
	}
	if isLeader() {
		t.Fatal("follower (leader is replica 2) reported leadership")
	}
	if len(host.asked) != 1 || host.asked[0] != shard.ShardKV {
		t.Fatalf("leadership read from shards %v, want [%d]", host.asked, shard.ShardKV)
	}

	host.id = 1
	if !isLeader() {
		t.Fatal("the KV shard's leader did not report leadership")
	}
	for name, h := range map[string]*kvLeader{
		"unknown leader": {id: 1},
		"leader error":   {id: 1, known: true, err: errors.New("closed")},
	} {
		if pendingUserLeader(h, 1, false)() {
			t.Fatalf("%s: reported leadership", name)
		}
	}
}

func TestPendingUserLeader_StandaloneSweepsAlways(t *testing.T) {
	if pendingUserLeader(&kvLeader{id: 2, known: true}, 1, true) != nil {
		t.Fatal("standalone node got a leader predicate")
	}
	if pendingUserLeader(nil, 1, false) != nil {
		t.Fatal("node without a Raft host got a leader predicate")
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
