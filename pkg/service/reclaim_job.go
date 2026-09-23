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
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Reclaim jobs (go-qrdb#547)
//
// TRUNCATE TABLE publishes a new table generation in one Raft transaction
// (TableService.TruncateTable) rather than scanning and deleting the old
// generation's rows and index entries inline -- that is what makes it O(1).
// The old generation's keys are still there, orphaned: nothing that resolves
// the table normally reads them again, yet they occupy storage until
// something deletes them. DROP INDEX orphans the dropped incarnation's
// entries the same way (TableService.DropIndex), and DROP TABLE the current
// generation's rows and index entries (TableService.DropTable). RENAME INDEX
// orphans nothing: entries are keyed by incarnation, not name.
//
// A ReclaimJob is the durable record of that orphan, written in the SAME
// transaction as the operation that orphans it, in the SAME store as the
// orphaned keys -- so there is no instant at which the orphan exists but
// nothing in its store remembers it. ReclaimSweeper is the background
// process that drains these records.
//
// The job's own store is not always one the sweeper can list cheaply: a
// shared-store database's keys are composite-namespaced under
// "<tenant>/<db>/", and a dedicated database's keys live in a store of its
// own. A ReclaimIndexEntry in the SYSTEM store, un-namespaced, is what lets
// the sweeper discover every pending job without enumerating every (tenant,
// database) pair. See ReclaimIndexEntry for the publication ordering between
// the two and why it is safe.
// ---------------------------------------------------------------------------

// reclaimJobPrefix is the key space holding pending ReclaimJob records, in
// whatever store (and namespace) the orphaned keys themselves live in.
const reclaimJobPrefix = "__reclaim:job/"

// reclaimJobKey returns the key holding one pending reclaim job.
func reclaimJobKey(id string) string {
	return reclaimJobPrefix + id
}

// ReclaimJob is a durable record of key prefixes that are orphaned and are
// deleted once no reader can still be using them. See the package doc
// comment above.
type ReclaimJob struct {
	// ID uniquely identifies the job. It is unique per publishing ATTEMPT,
	// not merely per orphaned keyspace (see newReclaimJobID): two attempts
	// never share an ID, which is what lets an attempt that definitely lost
	// its compare-and-swap withdraw its own index entry without any chance
	// of withdrawing another attempt's.
	ID string `json:"id"`

	// TenantID and Database identify the store the job and its KeyPrefixes
	// live in, resolved through the same path production writes use (see
	// ReclaimStoreResolver).
	TenantID string `json:"tenant_id"`
	Database string `json:"database"`

	// KeyPrefixes are the key prefixes to delete, relative to the resolved
	// store. The job is complete, and its own record removed, once no key
	// remains under any of them.
	KeyPrefixes []string `json:"key_prefixes"`

	// CreatedAt records when the job was published. It is informational:
	// reclamation is gated on when a sweeper OBSERVED the committed job,
	// never on this publisher-side clock (see ReclaimSweeper).
	CreatedAt time.Time `json:"created_at"`
}

// ReclaimJobOp encodes job as the Op a caller composes into its OWN
// transaction, alongside the operation that orphans the keyspace, so the job
// record commits atomically with it.
func ReclaimJobOp(job ReclaimJob) (Op, error) {
	data, err := json.Marshal(job)
	if err != nil {
		return Op{}, &ReclaimJobEncodeError{ID: job.ID, Err: err}
	}
	return Op{Type: OpPut, Key: []byte(reclaimJobKey(job.ID)), Value: data}, nil
}

// newReclaimJobID returns a job ID that is unique per call: a readable label
// naming what was orphaned (for operators reading the index), qualified by
// the tenant -- a dedicated store's TableIDs are allocated per store, so two
// tenants routinely produce the identical label -- and 128 random bits, so
// no two publishing attempts can ever collide on one index key.
func newReclaimJobID(tenantID, label string) string {
	return tenantID + ":" + label + ":" + rand.Text()
}

// StorePlacement classifies where a (tenant, database) pair's store lives,
// which decides which node may sweep that store's reclaim jobs.
type StorePlacement string

const (
	// StorePlacementShared is the replicated system store, composite-
	// namespaced per (tenant, database). The system shard's leader sweeps it.
	StorePlacementShared StorePlacement = "shared"

	// StorePlacementNode is a store local to ONE node -- a volume-isolated
	// tenant's single-node Host, or a database with its own dedicated
	// encrypting store. Only the node that published the job can reach it,
	// so only that node sweeps it (see ReclaimIndexEntry.Origin).
	StorePlacementNode StorePlacement = "node"

	// StorePlacementShard is a database's own dedicated Raft group. That
	// group's current leader sweeps it.
	StorePlacementShard StorePlacement = "shard"
)

// reclaimIndexPrefix is the key space, in the system store and never
// namespaced, holding pending ReclaimIndexEntry records.
const reclaimIndexPrefix = "__reclaim:index/"

// reclaimIndexKey returns the system-store key holding one pending job's
// index entry.
func reclaimIndexKey(jobID string) string {
	return reclaimIndexPrefix + jobID
}

// ReclaimFence names one key the publishing transaction compared by
// ModRevision, and the ModRevision it compared against.
type ReclaimFence struct {
	Key         string `json:"key"`
	ModRevision int64  `json:"mod_revision"`
}

// ReclaimIndexEntry is the system store's pointer to one pending ReclaimJob.
//
// It is ALWAYS written first, as its own transaction against the system
// store, strictly before the transaction that publishes the job -- the two
// stores cannot share one compare-and-swap, and this order means a crash
// between them leaves at worst an entry with no job behind it, never a job
// nothing points to (which would be an unreachable leak).
//
// "Entry, no job" is also exactly what an ordinary in-flight publish looks
// like between its two transactions, and what a finished job looks like if
// the sweeper crashed between deleting the job and deleting its entry.
// Fences are what tell those apart without guessing from elapsed time: they
// are keys the publishing transaction compared by ModRevision, chosen by the
// publisher so that (a) once any of them reads a different ModRevision in the
// job's store, that transaction can never commit, and (b) the transaction
// itself writes at least one of them, so committing moves a fence. While
// every fence holds, the job has not been published and the sweeper leaves
// the entry alone. Once one moves, the sweeper reads the job: present, it is
// drained as usual; absent, there is nothing left to find and the entry is
// deleted. The fences are read BEFORE the job, never after -- see
// ReclaimSweeper.sweepEntry.
type ReclaimIndexEntry struct {
	// JobID matches the ReclaimJob.ID this entry points to.
	JobID string `json:"job_id"`

	// TenantID and Database are what the job's store is resolved from.
	TenantID string `json:"tenant_id"`
	Database string `json:"database"`

	// Placement records where the job's store lived when it was published;
	// it decides which node sweeps the job (see ReclaimStoreResolver.Owns).
	Placement StorePlacement `json:"placement"`

	// Origin identifies the node that published the entry. It is what
	// routes a StorePlacementNode job back to the one node that holds it.
	Origin string `json:"origin"`

	// Fences are the publishing transaction's own compare targets -- see the
	// type doc comment.
	Fences []ReclaimFence `json:"fences"`

	// CreatedAt records when the entry was published (informational).
	CreatedAt time.Time `json:"created_at"`
}

// ReclaimIndexOp encodes entry as the Op a caller proposes against the
// system store.
func ReclaimIndexOp(entry ReclaimIndexEntry) (Op, error) {
	data, err := json.Marshal(entry)
	if err != nil {
		return Op{}, &ReclaimJobEncodeError{ID: entry.JobID, Err: err}
	}
	return Op{Type: OpPut, Key: []byte(reclaimIndexKey(entry.JobID)), Value: data}, nil
}

// ReclaimIndexStore is the system store as the sweeper uses it: a bounded
// Range over the index, and Get/Delete of individual entries.
// MVCCKVServicer satisfies it.
type ReclaimIndexStore interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	Range(ctx context.Context, start, end []byte, opts ...RangeOption) ([]*KeyValue, int64, int64, error)
}

// ReclaimStore is one job's resolved store, addressed in the same (possibly
// namespaced) keyspace the job was published in: Get and Delete read and
// remove the job record, Range lists the orphaned keys in bounded pages so a
// sweep never materializes a whole generation's key set, Execute deletes
// each page in ONE transaction rather than one proposal per key, and
// GetRevision reads a fence key's ModRevision. NewReclaimStore composes it
// from the pair a TableService is built over.
type ReclaimStore interface {
	ReclaimIndexStore
	GetRevision(ctx context.Context, key string) (*KeyValue, error)
	Execute(ctx context.Context, compares []Compare, successOps, failureOps []Op) (*TransactionResponse, error)
}

// NewReclaimStore composes a database's MVCC view and its transaction
// service -- the same pair, in the same keyspace, the TableService that
// published the job commits through -- into the store a sweep runs against.
func NewReclaimStore(mvcc MVCCKVServicer, txn TransactionServicer) ReclaimStore {
	return &reclaimStore{MVCCKVServicer: mvcc, txn: txn}
}

// reclaimStore is NewReclaimStore's ReclaimStore.
type reclaimStore struct {
	MVCCKVServicer
	txn TransactionServicer
}

// Execute proposes one transaction through the database's transaction
// service.
func (s *reclaimStore) Execute(ctx context.Context, compares []Compare, successOps, failureOps []Op) (*TransactionResponse, error) {
	return s.txn.Execute(ctx, compares, successOps, failureOps)
}

// ReclaimStoreResolver routes one index entry to the node that must sweep it
// and to its store, through the same path production writes take (pkg/server
// wires it; see ReclaimSweeper.SetStoreResolver).
type ReclaimStoreResolver interface {
	// Owns reports whether THIS node sweeps entry: for StorePlacementShared,
	// systemLeader; for StorePlacementNode, whether entry.Origin is this
	// node; for StorePlacementShard, whether this node currently leads the
	// database's own Raft group. A placement it does not recognize has no
	// owner. A dedicated group that no longer exists has no leader, so its
	// entries have no owner either (see the residuals in
	// docs/sql/engineering.md).
	Owns(entry ReclaimIndexEntry, systemLeader bool) bool

	// ReclaimStore resolves entry's store. It returns *ReclaimStoreGoneError
	// only when the (tenant, database) the entry names definitively no longer
	// exists -- the reclamation that removed it owns that keyspace, so the
	// entry is finished. Any other error is transient and retried.
	ReclaimStore(ctx context.Context, entry ReclaimIndexEntry) (ReclaimStore, error)
}

// DefaultReclaimInterval is the default interval ReclaimSweeper.Run sweeps
// at. Every node runs the loop -- ownership is per entry, see
// ReclaimStoreResolver.Owns -- and a tick that owns nothing costs one bounded
// Range over the index.
const DefaultReclaimInterval = 5 * time.Second

// reclaimQuiescenceMargin is added to TableGenerationPinTTL to absorb
// scheduling delay and clock-rate differences between the node that ran a
// reader and the node sweeping; neither clock's wall time is ever compared,
// only each node's own elapsed (monotonic) time.
const reclaimQuiescenceMargin = 5 * time.Second

// ReclaimQuiescence is how long a sweeper waits, measured from when IT FIRST
// OBSERVED a committed job, before deleting any of that job's keys.
//
// This is the reader-quiescence guarantee. A reader that resolved a table
// before the orphaning transaction committed pinned the old keyspace at a
// time P earlier than that commit; the SQL layer never returns a result read
// under a pin older than TableGenerationPinTTL without first re-proving the
// pin is still current (pkg/sql/provider), and re-proving fails with the
// typed *TableGenerationStaleError once the generation has moved. A sweeper
// cannot observe the job before it commits, so it deletes no earlier than
// P + ReclaimQuiescence -- after every result read under that pin was
// complete. Observation is in-memory and per sweeper: a new owner (leader
// change, restart) starts its own wait, which is only ever more
// conservative.
const ReclaimQuiescence = TableGenerationPinTTL + reclaimQuiescenceMargin

// reclaimIndexPageSize bounds how many index entries one SweepOnce reads.
// Later entries are reached on later ticks: the sweep resumes after the last
// key it read and wraps at the end, so no entry -- however many failing ones
// sort before it -- is starved.
const reclaimIndexPageSize = 64

// reclaimMaxKeysPerSweep bounds how many orphaned keys one SweepOnce lists
// and deletes across all jobs; reclaimKeyPageSize bounds a single listing.
// Neither the index nor a job's keyspace is ever listed whole.
const (
	reclaimMaxKeysPerSweep = 2000
	reclaimKeyPageSize     = 500
)

// ReclaimSweeper drains pending ReclaimJob records.
//
// It is constructed once per node (pkg/server/lifecycle.BuildServices) and
// every composer starts its Run loop on every node; which node acts on which
// job is decided per entry (ReclaimStoreResolver.Owns). Two sweepers acting
// on one job at once -- a leader change mid-sweep -- is harmless: deletes are
// idempotent and each sweeper enforces ReclaimQuiescence from its own
// observation.
type ReclaimSweeper struct {
	// index is the SYSTEM store, un-namespaced: where every
	// ReclaimIndexEntry lives.
	index ReclaimIndexStore

	// resolver is installed by the server after construction (see
	// SetStoreResolver) and read on every sweep, lock-free.
	resolver atomic.Pointer[reclaimResolverBox]

	// now is the sweeper's clock. time.Now carries a monotonic reading, so
	// elapsed-time comparisons never depend on wall-clock adjustments.
	now func() time.Time

	// sweeping admits one SweepOnce at a time. The fields below it are
	// touched only by the goroutine that won it, and the atomic handoff
	// orders one sweep's writes before the next sweep's reads.
	sweeping atomic.Bool

	// cursor is the last index key the previous sweep read; the next page
	// starts after it. Empty means start from the beginning.
	cursor string

	// progress records, per job ID, when this sweeper first saw its
	// committed job record -- the start of its ReclaimQuiescence wait -- and
	// where its deletion stopped, so the next sweep resumes after the last
	// key it deleted instead of re-listing (and re-walking the tombstones
	// of) everything before it.
	progress map[string]*reclaimProgress

	// cycleSeen collects the job IDs seen since the cursor last wrapped, so
	// progress can be pruned of jobs another sweeper finished.
	cycleSeen map[string]struct{}
}

// reclaimProgress is one job's in-memory sweep state; see
// ReclaimSweeper.progress.
type reclaimProgress struct {
	firstSeen time.Time

	// prefix indexes the job's KeyPrefixes entry deletion is working
	// through; after is the last key deleted under it ("" to start at the
	// prefix itself).
	prefix int
	after  string
}

// reclaimResolverBox lets an interface value be stored atomically.
type reclaimResolverBox struct {
	r ReclaimStoreResolver
}

// ReclaimSweeperOption configures optional ReclaimSweeper behavior.
type ReclaimSweeperOption func(*ReclaimSweeper)

// WithReclaimClock replaces the sweeper's clock. The quiescence wait itself
// is not configurable -- it is what keeps a reclaim behind every reader --
// so a test that needs a job drained advances the clock instead.
func WithReclaimClock(now func() time.Time) ReclaimSweeperOption {
	return func(rs *ReclaimSweeper) { rs.now = now }
}

// NewReclaimSweeper returns a sweeper over index, the un-namespaced system
// store. It resolves nothing until SetStoreResolver installs a resolver.
func NewReclaimSweeper(index ReclaimIndexStore, opts ...ReclaimSweeperOption) *ReclaimSweeper {
	rs := &ReclaimSweeper{
		index:     index,
		now:       time.Now,
		progress:  make(map[string]*reclaimProgress),
		cycleSeen: make(map[string]struct{}),
	}
	for _, opt := range opts {
		opt(rs)
	}
	return rs
}

// SetStoreResolver installs the resolver that routes each entry to its
// owner and store. It is safe to call while Run is sweeping: a sweep that
// starts before it is installed resolves nothing and deletes nothing.
func (rs *ReclaimSweeper) SetStoreResolver(r ReclaimStoreResolver) {
	if r == nil {
		rs.resolver.Store(nil)
		return
	}
	rs.resolver.Store(&reclaimResolverBox{r: r})
}

// storeResolver returns the installed resolver, or nil.
func (rs *ReclaimSweeper) storeResolver() ReclaimStoreResolver {
	if box := rs.resolver.Load(); box != nil {
		return box.r
	}
	return nil
}

// Run sweeps every interval until ctx is done. isLeader reports whether this
// node leads the system shard (nil means always, for a single-node
// composition); it decides ownership of StorePlacementShared entries only --
// the loop itself runs on every node, because node-local and dedicated-group
// jobs are owned by other nodes than the system leader.
func (rs *ReclaimSweeper) Run(ctx context.Context, interval time.Duration, isLeader func() bool, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rs.SweepOnce(ctx, isLeader, logger)
		}
	}
}

// SweepOnce reads one bounded page of index entries and advances every entry
// this node owns: first sight of a committed job starts its quiescence wait;
// a job past its wait has up to the sweep's remaining key budget deleted,
// and its record and entry removed once nothing is left under its prefixes.
// Anything unfinished is resumed on a later sweep; nothing assumes one call
// finishes a job. A call made while another is in progress returns
// immediately.
func (rs *ReclaimSweeper) SweepOnce(ctx context.Context, isLeader func() bool, logger *slog.Logger) {
	if !rs.sweeping.CompareAndSwap(false, true) {
		return
	}
	defer rs.sweeping.Store(false)
	if logger == nil {
		logger = slog.Default()
	}
	if rs.index == nil {
		return
	}
	resolver := rs.storeResolver()
	if resolver == nil {
		logger.Debug("reclaim sweep: no store resolver installed yet, skipping")
		return
	}

	page, err := rs.nextIndexPage(ctx)
	if err != nil {
		logger.Warn("reclaim sweep: list pending index entries failed", "error", err)
		return
	}
	systemLeader := isLeader == nil || isLeader()
	budget := reclaimMaxKeysPerSweep
	for _, kv := range page {
		if ctx.Err() != nil {
			return
		}
		indexKey := string(kv.Key)
		var entry ReclaimIndexEntry
		if err := json.Unmarshal(kv.Value, &entry); err != nil {
			logger.Warn("reclaim sweep: corrupt index entry, leaving for manual inspection", "key", indexKey, "error", err)
			continue
		}
		rs.cycleSeen[entry.JobID] = struct{}{}
		if !resolver.Owns(entry, systemLeader) {
			continue
		}
		store, err := resolver.ReclaimStore(ctx, entry)
		if err != nil {
			var gone *ReclaimStoreGoneError
			if errors.As(err, &gone) {
				rs.finishEntry(ctx, indexKey, entry.JobID, logger)
				continue
			}
			logger.Warn("reclaim sweep: resolving job store failed, will retry",
				"job_id", entry.JobID, "tenant", entry.TenantID, "database", entry.Database, "error", err)
			continue
		}
		budget -= rs.sweepEntry(ctx, indexKey, entry, store, budget, logger)
	}
}

// nextIndexPage reads up to reclaimIndexPageSize index entries after the
// cursor. A short page means the end of the index was reached: the cursor
// wraps to the beginning, and progress is pruned of every job not seen
// during the cycle that just ended (finished by another sweeper, or gone).
func (rs *ReclaimSweeper) nextIndexPage(ctx context.Context) ([]*KeyValue, error) {
	start := []byte(reclaimIndexPrefix)
	if rs.cursor != "" {
		start = append([]byte(rs.cursor), 0x00)
	}
	page, _, _, err := rs.index.Range(ctx, start, prefixEnd([]byte(reclaimIndexPrefix)),
		WithLimit(reclaimIndexPageSize))
	if err != nil {
		return nil, err
	}
	if len(page) < reclaimIndexPageSize {
		rs.cursor = ""
		for id := range rs.progress {
			if _, seen := rs.cycleSeen[id]; !seen {
				delete(rs.progress, id)
			}
		}
		clear(rs.cycleSeen)
	} else {
		rs.cursor = string(page[len(page)-1].Key)
	}
	return page, nil
}

// sweepEntry advances one owned entry and returns how many keys it deleted.
func (rs *ReclaimSweeper) sweepEntry(ctx context.Context, indexKey string, entry ReclaimIndexEntry, store ReclaimStore, budget int, logger *slog.Logger) int {
	if store == nil {
		logger.Warn("reclaim sweep: resolver returned no store, will retry", "job_id", entry.JobID)
		return 0
	}
	// Fences first, then the job: a job read first and found absent could
	// be committed by its publish an instant later, which the fence read
	// that followed would then report as settled -- and the entry naming a
	// live job would be deleted. Read in this order, a moved fence means the
	// publish can never commit again, so a job still absent after it has
	// nothing left to find. An entry with no fences is never settled by
	// absence.
	if len(entry.Fences) > 0 {
		open, err := publishMayCommit(ctx, store, entry.Fences)
		if err != nil {
			logger.Warn("reclaim sweep: reading publish fences failed, will retry", "job_id", entry.JobID, "error", err)
			return 0
		}
		if open {
			// Its publish has not committed: there is no job yet.
			return 0
		}
	}
	jobData, err := store.Get(ctx, reclaimJobKey(entry.JobID))
	if err != nil && !isKeyNotFound(err) {
		logger.Warn("reclaim sweep: read job record failed", "job_id", entry.JobID, "error", err)
		return 0
	}
	if jobData == nil {
		if len(entry.Fences) > 0 {
			rs.finishEntry(ctx, indexKey, entry.JobID, logger)
		}
		return 0
	}
	var job ReclaimJob
	if err := json.Unmarshal(jobData, &job); err != nil {
		logger.Warn("reclaim sweep: corrupt job record, leaving for manual inspection", "job_id", entry.JobID, "error", err)
		return 0
	}

	now := rs.now()
	prog, seen := rs.progress[job.ID]
	if !seen {
		rs.progress[job.ID] = &reclaimProgress{firstSeen: now}
		return 0
	}
	if now.Sub(prog.firstSeen) < ReclaimQuiescence || budget <= 0 {
		return 0
	}

	deleted, drained, err := reclaimKeysBounded(ctx, store, job.KeyPrefixes, budget, prog)
	if err != nil {
		logger.Warn("reclaim sweep: delete failed, will retry", "job_id", job.ID, "error", err)
		return deleted
	}
	if !drained {
		return deleted
	}
	if err := store.Delete(ctx, reclaimJobKey(job.ID)); err != nil {
		logger.Warn("reclaim sweep: job record delete failed, will retry", "job_id", job.ID, "error", err)
		return deleted
	}
	// The entry goes last: once the job record is gone, the fence check at
	// the top of this method is what lets a crash between these two deletes
	// be finished by a later sweep.
	rs.finishEntry(ctx, indexKey, job.ID, logger)
	return deleted
}

// finishEntry deletes a finished job's index entry and forgets its
// observation.
func (rs *ReclaimSweeper) finishEntry(ctx context.Context, indexKey, jobID string, logger *slog.Logger) {
	if err := rs.index.Delete(ctx, indexKey); err != nil {
		logger.Warn("reclaim sweep: index entry delete failed, will retry", "job_id", jobID, "error", err)
		return
	}
	delete(rs.progress, jobID)
}

// publishMayCommit reports whether the transaction that would publish an
// entry's job can still commit: true while every fence key still reads the
// ModRevision the publisher compared against. Once one differs, that
// compare can never hold again, so the job either committed already or never
// will. An entry with no fences cannot be proven either way and is always
// reported open.
func publishMayCommit(ctx context.Context, r ReclaimStore, fences []ReclaimFence) (bool, error) {
	if len(fences) == 0 {
		return true, nil
	}
	for _, f := range fences {
		var modRevision int64
		kv, err := r.GetRevision(ctx, f.Key)
		switch {
		case err == nil && kv != nil:
			modRevision = kv.ModRevision
		case err == nil, isKeyNotFound(err):
		default:
			return false, err
		}
		if modRevision != f.ModRevision {
			return false, nil
		}
	}
	return true, nil
}

// reclaimKeysBounded deletes keys under prefixes, listing them in pages of
// at most reclaimKeyPageSize -- each page deleted in one transaction, one
// Raft proposal, not one per key -- and stopping once budget keys are
// deleted. It
// resumes from prog -- the prefix it was working through and the last key it
// deleted there -- and advances prog as it goes, so no sweep re-lists (or
// re-walks the tombstones of) keys an earlier sweep already deleted. It
// reports drained only when every prefix has been listed to its end.
func reclaimKeysBounded(ctx context.Context, store ReclaimStore, prefixes []string, budget int, prog *reclaimProgress) (deleted int, drained bool, err error) {
	for ; prog.prefix < len(prefixes); prog.prefix, prog.after = prog.prefix+1, "" {
		prefix := prefixes[prog.prefix]
		if prefix == "" {
			return deleted, false, &ReclaimScopeError{}
		}
		end := prefixEnd([]byte(prefix))
		for {
			remaining := budget - deleted
			if remaining <= 0 {
				return deleted, false, nil
			}
			limit := min(remaining, reclaimKeyPageSize)
			start := []byte(prefix)
			if prog.after != "" {
				start = append([]byte(prog.after), 0x00)
			}
			page, _, _, lerr := store.Range(ctx, start, end, WithKeysOnly(), WithLimit(int64(limit)))
			if lerr != nil {
				return deleted, false, &ReclaimPrefixError{Prefix: prefix, Op: "list", Err: lerr}
			}
			if len(page) > 0 {
				ops := make([]Op, len(page))
				for i, kv := range page {
					ops[i] = Op{Type: OpDelete, Key: kv.Key}
				}
				// No compares: a key under an orphaned prefix is deleted
				// whatever it holds, so the transaction cannot lose.
				if _, derr := store.Execute(ctx, nil, ops, nil); derr != nil {
					return deleted, false, &ReclaimPrefixError{Prefix: prefix, Op: "delete", Err: derr}
				}
				deleted += len(page)
				prog.after = string(page[len(page)-1].Key)
			}
			if len(page) < limit {
				break
			}
		}
	}
	return deleted, true, nil
}
