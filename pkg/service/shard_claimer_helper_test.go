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
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// testShardClaimer is the shard ID issuer every production Manager has: a
// shard.Claimer over a replicated store, here an in-memory one. Unconstrained
// so every test file in the package can use it.
func testShardClaimer(t *testing.T) *shard.Claimer {
	t.Helper()
	c, err := shard.NewClaimer(kvstore.NewMemoryKVStore())
	if err != nil {
		t.Fatalf("shard.NewClaimer: %v", err)
	}
	return c
}

// claimingManager builds a database.Manager the way lifecycle.BuildServices
// does: constructed first, then given its shard-ID claimer.
func claimingManager(t *testing.T, logger *slog.Logger, dir string) *database.Manager {
	t.Helper()
	m := database.NewManager(logger, dir)
	m.SetShardIDClaimer(testShardClaimer(t))
	return m
}
