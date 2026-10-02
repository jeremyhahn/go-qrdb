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

package config

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// A node's own raft.shard_id is the system group's: it must sit below the
// user range, whose IDs the cluster issues to databases. A node configured
// into the user range could run its system group on an ID a database holds.
func TestValidate_RaftShardIDBelowTheUserRange(t *testing.T) {
	for _, id := range []uint64{shard.UserMin, shard.UserMin + 1} {
		cfg := validConfig()
		cfg.Raft.ShardID = id
		err := cfg.Validate()
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Field != "raft.shard_id" {
			t.Fatalf("raft.shard_id %d = %v, want a ValidationError on raft.shard_id", id, err)
		}
	}
	cfg := validConfig()
	cfg.Raft.ShardID = shard.UserMin - 1
	if err := cfg.Validate(); err != nil {
		t.Fatalf("raft.shard_id %d: %v", shard.UserMin-1, err)
	}
}
