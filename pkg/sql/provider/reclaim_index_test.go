//go:build qrdb_sql

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

package provider

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// harnessReclaimResolver routes every reclaim job to the harness's one store.
type harnessReclaimResolver struct {
	store *memStore
	txn   service.TransactionServicer
}

func (r *harnessReclaimResolver) Owns(service.ReclaimIndexEntry, bool) bool { return true }

func (r *harnessReclaimResolver) ReclaimStore(context.Context, service.ReclaimIndexEntry) (service.ReclaimStore, error) {
	return service.NewReclaimStore(r.store, r.txn), nil
}

// sweepPastQuiescence drives a real ReclaimSweeper over the harness store
// until every pending reclaim job is observed, ReclaimQuiescence has passed,
// and the jobs are drained.
func sweepPastQuiescence(t *testing.T, h *ddlHarness) {
	t.Helper()
	now := time.Unix(1_700_000_000, 0)
	sweeper := service.NewReclaimSweeper(h.store, service.WithReclaimClock(func() time.Time { return now }))
	sweeper.SetStoreResolver(&harnessReclaimResolver{store: h.store, txn: h.txnSvc})
	for range 4 {
		sweeper.SweepOnce(context.Background(), nil, nil)
		now = now.Add(service.ReclaimQuiescence)
	}
	jobs, err := h.kvSvc.Scan(context.Background(), "__reclaim:job/")
	require.NoError(t, err)
	require.Empty(t, jobs, "every reclaim job must be drained")
}

// indexedUsers creates shop.users (id, email, code) with a UNIQUE index a on
// email and two rows, through SQL.
func indexedUsers(t *testing.T) (*ddlHarness, *Provider) {
	t.Helper()
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")
	mustExecQuery(t, prov, "shop", "CREATE TABLE users (id BIGINT UNSIGNED PRIMARY KEY, email VARCHAR(64), code VARCHAR(16))")
	mustExecQuery(t, prov, "shop", "CREATE UNIQUE INDEX a ON users (email)")
	mustExecQuery(t, prov, "shop", "INSERT INTO users VALUES (1, 'alice@example.com', 'A'), (2, 'bob@example.com', 'B')")
	return h, prov
}

// assertUniqueIndexIntact proves the live index named name still holds an
// entry for every row, answers lookupSQL with exactly one row, and still
// enforces UNIQUE: duplicateSQL must be refused.
func assertUniqueIndexIntact(t *testing.T, h *ddlHarness, prov *Provider, name, lookupSQL, duplicateSQL string) {
	t.Helper()
	def, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	entries, err := h.kvSvc.Scan(context.Background(),
		service.TableIndexPrefix("shop", def.TableID, def.Generation, incarnationOf(def, name)))
	require.NoError(t, err)
	assert.Len(t, entries, 2, "the live index %s must keep an entry for every row", name)

	_, rows := mustExecQuery(t, prov, "shop", lookupSQL)
	require.Len(t, rows, 1)

	_, _, err = execQuery(prov, "shop", duplicateSQL)
	require.Error(t, err, "UNIQUE on index %s must still be enforced", name)
	// The engine reports it with ErrUniqueKeyViolation's own message.
	assert.Contains(t, err.Error(), "duplicate unique key given", "err = %v must be a unique-key violation", err)
}

// TestReclaim_RenameThenRecreateTheOldName is the reuse hazard: RENAME
// INDEX a TO b, then CREATE INDEX a. Had index entries been keyed by name,
// reclaiming what "a" named before would delete the new "a"'s entries. With
// incarnation keys the two never share a keyspace; after every reclaim job
// has drained, both indexes are intact and UNIQUE is still enforced.
func TestReclaim_RenameThenRecreateTheOldName(t *testing.T) {
	h, prov := indexedUsers(t)
	mustExecQuery(t, prov, "shop", "ALTER TABLE users RENAME INDEX a TO b")
	mustExecQuery(t, prov, "shop", "CREATE UNIQUE INDEX a ON users (code)")
	mustExecQuery(t, prov, "shop", "TRUNCATE TABLE users")
	mustExecQuery(t, prov, "shop", "INSERT INTO users VALUES (1, 'alice@example.com', 'A'), (2, 'bob@example.com', 'B')")

	sweepPastQuiescence(t, h)

	assertUniqueIndexIntact(t, h, prov, "b",
		"SELECT id FROM users WHERE email = 'alice@example.com'",
		"INSERT INTO users VALUES (3, 'alice@example.com', 'C')")
	assertUniqueIndexIntact(t, h, prov, "a",
		"SELECT id FROM users WHERE code = 'B'",
		"INSERT INTO users VALUES (4, 'carol@example.com', 'B')")
}

// TestReclaim_RenameAwayAndBack is the second sequence: RENAME a TO b, then b
// TO a. The index keeps its incarnation throughout, so there is nothing to
// reclaim and nothing a reclaim could take from it.
func TestReclaim_RenameAwayAndBack(t *testing.T) {
	h, prov := indexedUsers(t)
	mustExecQuery(t, prov, "shop", "ALTER TABLE users RENAME INDEX a TO b")
	mustExecQuery(t, prov, "shop", "ALTER TABLE users RENAME INDEX b TO a")

	sweepPastQuiescence(t, h)

	assertUniqueIndexIntact(t, h, prov, "a",
		"SELECT id FROM users WHERE email = 'bob@example.com'",
		"INSERT INTO users VALUES (5, 'bob@example.com', 'E')")
}

// TestReclaim_DropThenRecreateTheSameName is the sequence that DOES publish
// a job for the old keyspace: DROP INDEX a, then CREATE INDEX a. The job
// reclaims the dropped incarnation only; the new "a" is untouched.
func TestReclaim_DropThenRecreateTheSameName(t *testing.T) {
	h, prov := indexedUsers(t)
	before, err := h.tableSvc.GetTable(context.Background(), "shop", "users")
	require.NoError(t, err)
	dropped := service.TableIndexPrefix("shop", before.TableID, 0, incarnationOf(before, "a"))

	mustExecQuery(t, prov, "shop", "DROP INDEX a ON users")
	mustExecQuery(t, prov, "shop", "CREATE UNIQUE INDEX a ON users (email)")

	sweepPastQuiescence(t, h)

	leftover, err := h.kvSvc.Scan(context.Background(), dropped)
	require.NoError(t, err)
	assert.Empty(t, leftover, "the dropped incarnation's entries are reclaimed")
	assertUniqueIndexIntact(t, h, prov, "a",
		"SELECT id FROM users WHERE email = 'alice@example.com'",
		"INSERT INTO users VALUES (6, 'alice@example.com', 'F')")
}
