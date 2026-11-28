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
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
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
	m := databasetest.NewManager(logger, dir)
	m.SetShardIDClaimer(testShardClaimer(t))
	return m
}

// requireErrorAs asserts that err's chain carries a T and returns it.
//
// It is the assertion the vector tests make about the service's typed error
// vocabulary (go-qrdb#331): the TYPE is the contract every transport
// classifies, so it is what a test pins — never a code field or a message.
// Untagged, because callers in every build-tag variant use it.
func requireErrorAs[T error](t *testing.T, err error) T {
	t.Helper()
	var target T
	require.ErrorAs(t, err, &target)
	return target
}

// putRefusingCatalog is a catalog whose every Update fails: a database record
// that cannot be written.
type putRefusingCatalog struct{ database.Catalog }

var _ database.Catalog = putRefusingCatalog{}

func (putRefusingCatalog) Update(context.Context, string, string, string, func(*database.DatabaseMeta) error) error {
	return catalogPutRefusedError{}
}

// catalogPutRefusedError is putRefusingCatalog's refusal.
type catalogPutRefusedError struct{}

func (catalogPutRefusedError) Error() string { return "catalog put refused" }
