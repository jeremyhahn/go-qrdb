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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	"github.com/jeremyhahn/go-quicraft/pkg/config"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// User-range shard IDs are issued by the cluster to databases; an admin
// start of one would run a group the claimer may hand to another database.
// StartShard refuses one before touching the host. Below the user range it
// starts as before.
func TestAdminService_StartShard_RefusesUserRangeIDs(t *testing.T) {
	for _, id := range []uint64{shard.UserMin, shard.UserMin + 7} {
		host := &mockHost{}
		started := false
		host.startShardFn = func(map[uint64]string, bool, sm.CreateFunc, config.Config) error {
			started = true
			return nil
		}
		svc := NewAdminService(host, WithStateMachineFactory(dummyCreateFunc))
		err := svc.StartShard(context.Background(), &StartShardRequest{ShardID: id, ReplicaID: 1})
		var userRange *UserRangeShardError
		if !errors.As(err, &userRange) || userRange.ShardID != id {
			t.Fatalf("StartShard(%d) = %v, want *UserRangeShardError", id, err)
		}
		if userRange.Error() == "" {
			t.Fatal("empty error text")
		}
		if started {
			t.Fatalf("StartShard(%d) reached the host", id)
		}
	}

	host := &mockHost{}
	host.startShardFn = func(map[uint64]string, bool, sm.CreateFunc, config.Config) error { return nil }
	svc := NewAdminService(host, WithStateMachineFactory(dummyCreateFunc))
	if err := svc.StartShard(context.Background(), &StartShardRequest{ShardID: shard.UserMin - 1, ReplicaID: 1}); err != nil {
		t.Fatalf("StartShard below the user range: %v", err)
	}
}
