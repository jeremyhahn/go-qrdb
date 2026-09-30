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

package tenant

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/validation"
)

// ---------------------------------------------------------------------------
// Reading the retirement record
//
// A retired id is permanent and replicated, so it is enumerable: an operator
// planning a migration or auditing what was deleted and when reads the
// tombstone range rather than discovering retirements one refused create at a
// time. The same reader is the ONLY predicate the deleted-tenant reconcile
// uses to decide a directory is reclaimable. A missing placement entry or a
// missing tenant record would also describe a tenant that is not placed yet,
// and reclaiming on that signal destroys live data; a tombstone describes
// nothing but a deletion.
// ---------------------------------------------------------------------------

const (
	// DefaultDeletedTenantsPageSize is the page size ListDeletedTenants uses
	// when the caller asks for 0.
	DefaultDeletedTenantsPageSize = 100

	// MaxDeletedTenantsPageSize is the largest page ListDeletedTenants
	// returns. A larger request is served at this size rather than refused,
	// and the next page token carries the caller on.
	MaxDeletedTenantsPageSize = 1000
)

// DeletedTenantsPage is one page of retired tenant ids, in ascending id order.
type DeletedTenantsPage struct {
	// Tombstones holds this page's records. A record whose body cannot be
	// decoded still appears, carrying only its ID: the id is retired either
	// way, and hiding it would misreport what is permanently unusable.
	Tombstones []Tombstone `json:"tombstones"`

	// NextPageToken resumes the listing after this page. It is empty on the
	// last page. It is opaque: callers pass it back unchanged.
	NextPageToken string `json:"next_page_token,omitempty"`
}

// tombstoneLister enumerates the storage keys under a prefix.
type tombstoneLister func(ctx context.Context, prefix string) ([]string, error)

// retiredIDs returns every tombstoned tenant id, sorted. The sort is explicit
// because not every store lists in key order, and the page token is an id
// bound that only means something over a total order.
func retiredIDs(ctx context.Context, list tombstoneLister) ([]string, error) {
	keys, err := list(ctx, tombstonePrefix)
	if err != nil {
		return nil, &StoreError{Op: "list-tombstones", Err: err}
	}
	ids := make([]string, 0, len(keys))
	for _, k := range keys {
		if id := strings.TrimPrefix(k, tombstonePrefix); id != "" && id != k {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// encodePageToken and decodePageToken turn the last id of a page into the
// opaque token that resumes after it.
func encodePageToken(lastID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(lastID))
}

func decodePageToken(token string) (string, error) {
	if token == "" {
		return "", nil
	}
	// Raw (unpadded) base64url never decodes a non-empty string to zero
	// bytes, so a decodable token always names an id to resume after. A token
	// that decodes is honoured whether or not this service minted it: it is
	// only a lower bound on the ids listed, and grants nothing.
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", &InvalidPageRequestError{Field: "page_token", Reason: "not a valid page token for this listing"}
	}
	return string(raw), nil
}

// resolvePageSize applies the page-size contract: negative is refused, zero
// means the default, and anything above the maximum is served at the maximum.
func resolvePageSize(size int) (int, error) {
	switch {
	case size < 0:
		return 0, &InvalidPageRequestError{Field: "page_size", Reason: "must not be negative"}
	case size == 0:
		return DefaultDeletedTenantsPageSize, nil
	case size > MaxDeletedTenantsPageSize:
		return MaxDeletedTenantsPageSize, nil
	default:
		return size, nil
	}
}

// listDeletedTenants is the one implementation of ListDeletedTenants behind
// both tenant services: a paged range read over the tombstone prefix, resumed
// strictly after the id the page token names.
//
// Cost, stated: neither store interface the tenant services run on
// (seal.StorageBackend, kvstore.KVStore) offers an ordered range scan bounded
// by a start key and a limit, so every page lists the KEYS of the whole
// tombstone prefix -- O(retired ids) -- and reads the VALUES of at most one
// page. The prefix is bounded by the number of deletions ever made, and the
// values, the larger half, are never read beyond the page.
func listDeletedTenants(ctx context.Context, list tombstoneLister, read tombstoneReader, pageToken string, pageSize int) (*DeletedTenantsPage, error) {
	size, err := resolvePageSize(pageSize)
	if err != nil {
		return nil, err
	}
	after, err := decodePageToken(pageToken)
	if err != nil {
		return nil, err
	}
	ids, err := retiredIDs(ctx, list)
	if err != nil {
		return nil, err
	}
	start := sort.SearchStrings(ids, after)
	if start < len(ids) && after != "" && ids[start] == after {
		start++
	}

	page := &DeletedTenantsPage{Tombstones: make([]Tombstone, 0, size)}
	i := start
	for ; i < len(ids) && len(page.Tombstones) < size; i++ {
		ts, present, rerr := readTombstone(ctx, read, ids[i])
		if rerr != nil {
			return nil, rerr
		}
		if present {
			page.Tombstones = append(page.Tombstones, ts)
		}
	}
	if i < len(ids) && len(page.Tombstones) > 0 {
		page.NextPageToken = encodePageToken(page.Tombstones[len(page.Tombstones)-1].ID)
	}
	return page, nil
}

// readTombstone reads one tombstone. present is false when the record vanished
// between the listing and the read, which is not an error for a listing. The
// key, not the body, is authoritative for the id.
func readTombstone(ctx context.Context, read tombstoneReader, id string) (Tombstone, bool, error) {
	data, err := read(ctx, tombstoneKey(id))
	if err != nil {
		if isAbsent(err) {
			return Tombstone{}, false, nil
		}
		return Tombstone{}, false, &StoreError{Op: "tombstone-get", Err: err}
	}
	if data == nil {
		return Tombstone{}, false, nil
	}
	var ts Tombstone
	if json.Unmarshal(data, &ts) != nil {
		ts = Tombstone{}
	}
	ts.ID = id
	return ts, true, nil
}

// ListDeletedTenants returns one page of retired tenant ids with their
// deletion records. See DeletedTenantsPage.
func (s *TenantService) ListDeletedTenants(ctx context.Context, pageToken string, pageSize int) (*DeletedTenantsPage, error) {
	return listDeletedTenants(ctx, s.store.List, s.store.Get, pageToken, pageSize)
}

// ListDeletedTenants returns one page of retired tenant ids with their
// deletion records. See DeletedTenantsPage.
func (s *kvTenantService) ListDeletedTenants(ctx context.Context, pageToken string, pageSize int) (*DeletedTenantsPage, error) {
	return listDeletedTenants(ctx, s.store.List, s.store.Get, pageToken, pageSize)
}

// ---------------------------------------------------------------------------
// Reclaiming a retired tenant's directories on this node
// ---------------------------------------------------------------------------

// TenantRuntimeCloser stops everything THIS node runs for a tenant -- its
// per-tenant host, its dedicated raft shards -- so the directories under them
// can be removed. It is the seam behind ReconcileDeletedTenants: the runtimes
// live in pkg/server, which imports this package. It must be idempotent: a
// tenant with nothing running is the success case.
type TenantRuntimeCloser interface {
	CloseTenantRuntime(ctx context.Context, tenantID string) error
}

// ResidueWiring is the post-construction wiring surface for the runtime
// closer, implemented by TenantService. A composition sets it once the
// runtimes exist, which is after the tenant service is built.
type ResidueWiring interface {
	SetTenantRuntimeCloser(TenantRuntimeCloser)
}

// Compile-time check.
var _ ResidueWiring = (*TenantService)(nil)

// SetTenantRuntimeCloser implements ResidueWiring.
func (s *TenantService) SetTenantRuntimeCloser(c TenantRuntimeCloser) {
	s.runtimeCloser.Store(&runtimeCloserBox{closer: c})
}

// TenantRuntimeCloser returns the runtime closer the composition wired, or nil
// when none is.
func (s *TenantService) TenantRuntimeCloser() TenantRuntimeCloser {
	if box := s.runtimeCloser.Load(); box != nil {
		return box.closer
	}
	return nil
}

// runtimeCloserBox lets the closer be swapped atomically: a composition that
// rebuilds its service graph re-wires it while requests may be reading it.
type runtimeCloserBox struct{ closer TenantRuntimeCloser }

// The reasons ReconcileDeletedTenants reports for leaving something in place.
const (
	// SkipLiveTenantRecord: the retired id ALSO has a tenant record. The two
	// should never coexist; while they do, nothing of the id's is reclaimed,
	// because the record is the stronger claim.
	SkipLiveTenantRecord = "live-tenant-record"

	// SkipOverlapsLiveTenant: the directory is, contains or lies inside a
	// directory a live tenant claims -- an operator-supplied DataRoot can be
	// reused by a later tenant, and a stale record on a node that missed the
	// delete would otherwise name the new tenant's data.
	SkipOverlapsLiveTenant = "overlaps-live-tenant"

	// SkipOverlapsReservedRoot: the directory is, or contains, a volume-pool
	// base, or it is, contains or lies inside a reserved system data root.
	SkipOverlapsReservedRoot = "overlaps-reserved-root"

	// SkipNotCanonical: the directory is not an absolute, canonical path -- a
	// recorded root that is relative, or carries "." or ".." segments, names
	// something other than what it appears to, and is never removed.
	SkipNotCanonical = "not-canonical"
)

// ReclaimedPath is a directory ReconcileDeletedTenants removed.
type ReclaimedPath struct {
	TenantID string `json:"tenant_id"`
	Path     string `json:"path"`
}

// SkippedPath is something ReconcileDeletedTenants left in place, and why.
type SkippedPath struct {
	TenantID string `json:"tenant_id"`
	// Path is empty when the whole id was skipped (SkipLiveTenantRecord).
	Path   string `json:"path,omitempty"`
	Reason string `json:"reason"`
}

// ReconcileReport describes one ReconcileDeletedTenants pass on one node.
type ReconcileReport struct {
	// Inspected is the number of retired ids examined.
	Inspected int `json:"inspected"`
	// Reclaimed lists the directories removed from this node.
	Reclaimed []ReclaimedPath `json:"reclaimed,omitempty"`
	// Skipped lists what was left in place.
	Skipped []SkippedPath `json:"skipped,omitempty"`
	// Failed lists the retired ids the pass could not fully reclaim. It is
	// the report's copy of the *ResidueReconcileFailuresError the pass returns with
	// it, so a transport can answer with one response carrying both what was
	// done and what was not.
	Failed []FailedReclaim `json:"failed,omitempty"`
}

// FailedReclaim is one retired id ReconcileDeletedTenants could not fully
// reclaim. The id keeps its records, so the next pass retries it.
type FailedReclaim struct {
	TenantID string `json:"tenant_id"`
	// Op is the step that failed ("remove", "close-runtime", ...).
	Op string `json:"op"`
	// Path is empty when the step is not about one path.
	Path string `json:"path,omitempty"`
	// Error is the failure's message.
	Error string `json:"error"`
}

// ReconcileResponseError is the error a transport answers
// ReconcileDeletedTenants with INSTEAD of the report. It is nil when err is
// nil, and when err only collects per-id failures that report.Failed already
// carries: the transport then answers with the report, whose Failed list
// tells the caller what was not reclaimed. Any other error -- a guard that
// could not be read -- is returned as is.
func ReconcileResponseError(report *ReconcileReport, err error) error {
	var agg *ResidueReconcileFailuresError
	if err == nil || (report != nil && errors.As(err, &agg) && len(report.Failed) == len(agg.Errors)) {
		return nil
	}
	return err
}

// residueGuards is what one reconcile pass checks every path against.
type residueGuards struct {
	live      []string // directories live tenants claim
	poolBases []string // a removed path must not be or contain one
	reserved  []string // a removed path must not overlap one
}

// ReconcileDeletedTenants removes, from THIS node, the on-disk residue of
// every retired tenant id: the directories a replica keeps when it was down,
// or not hosting the shard, when the tenant was deleted. It is what the boot
// pass runs before any barrier is restored, and what the operator verb runs
// on demand.
//
// The only reclaim predicate is the TOMBSTONE, and the guard is the LIVE set,
// read strictly: a tenant record that cannot be decoded aborts the pass,
// because its DataRoot is unknown and could be any candidate here. Per retired
// id it skips the id whole if a tenant record exists for it, skips an id this
// node has already fully reclaimed, stops whatever this node runs for it,
// releases its in-memory barrier, removes each local directory the registry
// attributes to it -- never one that is not canonical AS RECORDED, that a
// live tenant claims, or that is or contains a pool base or reserved root, and
// re-reading the live set right before each removal -- and only then drops
// the durable records that name those directories.
//
// Failures split in two. A failure of the guards -- listing the tombstones,
// reading the live set -- aborts the pass, because nothing can be judged
// without them. A failure for ONE id -- a removal, stopping its runtime,
// dropping its records -- is collected and the pass goes on to the next id;
// the pass then returns its report together with *ResidueReconcileFailuresError. An
// id whose removal failed keeps its records, so the next pass finds the
// directory again. Every step is idempotent.
func (s *TenantService) ReconcileDeletedTenants(ctx context.Context) (*ReconcileReport, error) {
	ids, err := retiredIDs(ctx, s.store.List)
	if err != nil {
		return nil, &ResidueReconcileError{Op: "list-tombstones", Err: err}
	}
	report := &ReconcileReport{}
	if len(ids) == 0 {
		return report, nil
	}
	guards, err := s.residueGuards(ctx)
	if err != nil {
		return nil, err
	}
	var failed []*ResidueReconcileError
	for _, id := range ids {
		report.Inspected++
		perID, abort := s.reconcileRetired(ctx, id, guards, report)
		if abort != nil {
			return report, abort
		}
		failed = append(failed, perID...)
	}
	if len(report.Reclaimed) > 0 || len(report.Skipped) > 0 || len(failed) > 0 {
		s.logger.Info("deleted-tenant reconcile",
			slog.Int("inspected", report.Inspected),
			slog.Int("reclaimed", len(report.Reclaimed)),
			slog.Int("skipped", len(report.Skipped)),
			slog.Int("failed", len(failed)))
	}
	if len(failed) > 0 {
		for _, f := range failed {
			report.Failed = append(report.Failed, FailedReclaim{TenantID: f.TenantID, Op: f.Op, Path: f.Path, Error: f.Err.Error()})
		}
		return report, &ResidueReconcileFailuresError{Errors: failed}
	}
	return report, nil
}

// residueGuards reads the live set strictly and the reserved roots, once per
// pass.
func (s *TenantService) residueGuards(ctx context.Context) (*residueGuards, error) {
	live, err := s.liveTenantRootsStrict(ctx)
	if err != nil {
		return nil, &ResidueReconcileError{Op: "list-tenants", Err: err}
	}
	bases, err := s.registry.PoolBasePaths(ctx)
	if err != nil {
		return nil, &ResidueReconcileError{Op: "candidates", Err: err}
	}
	return &residueGuards{live: live, poolBases: bases, reserved: s.reservedDataRoots}, nil
}

// reconcileRetired reclaims one retired id's residue on this node. It returns
// the per-id failures it collected, and a non-nil abort only when a guard
// could not be read.
func (s *TenantService) reconcileRetired(ctx context.Context, id string, g *residueGuards, report *ReconcileReport) ([]*ResidueReconcileError, error) {
	fail := func(op, path string, err error) []*ResidueReconcileError {
		return []*ResidueReconcileError{{TenantID: id, Op: op, Path: path, Err: err}}
	}

	// A direct read of the id's own record, not a scan: an undecodable record
	// still EXISTS, and existence is what stops the reclamation.
	if _, err := s.store.Get(ctx, metadataPrefix+id); err == nil {
		s.skip(report, id, "", SkipLiveTenantRecord)
		return nil, nil
	} else if !isAbsent(err) {
		return fail("live-record", "", err), nil
	}

	candidates, err := s.registry.TenantDataRootCandidates(ctx, id)
	if err != nil {
		return fail("candidates", "", err), nil
	}
	if done, err := s.fullyReclaimed(ctx, id, candidates); err != nil {
		return fail("candidates", "", err), nil
	} else if done {
		return nil, nil
	}

	if closer := s.TenantRuntimeCloser(); closer != nil {
		if err := closer.CloseTenantRuntime(ctx, id); err != nil {
			// Never remove a directory from under a runtime that would not
			// stop.
			return fail("close-runtime", "", err), nil
		}
	}
	if err := s.registry.ReleaseTenant(ctx, id); err != nil && !errors.Is(err, seal.ErrTenantNotFound) {
		return fail("release-barrier", "", err), nil
	}

	var failed []*ResidueReconcileError
	for _, path := range candidates {
		reason, perr, abort := s.removeResidue(ctx, id, path, g)
		if abort != nil {
			return failed, abort
		}
		if perr != nil {
			failed = append(failed, perr)
			continue
		}
		switch reason {
		case "":
			report.Reclaimed = append(report.Reclaimed, ReclaimedPath{TenantID: id, Path: path})
			s.logger.Info("reclaimed deleted tenant directory",
				slog.String("tenant_id", id),
				slog.String("path", path))
		case reasonAbsent:
		default:
			s.skip(report, id, path, reason)
		}
	}
	if len(failed) > 0 {
		// A directory that could not be removed keeps the records that name
		// it, so the next pass finds it again.
		return failed, nil
	}

	// Last: only now are the records that name those directories dropped.
	if err := s.registry.DropTenantRecords(ctx, id); err != nil {
		return fail("drop-records", "", err), nil
	}
	return nil, nil
}

// skip records a path left in place and logs it at Warn: it is disk the
// operator still owns, and a reason they may need to act on.
func (s *TenantService) skip(report *ReconcileReport, id, path, reason string) {
	report.Skipped = append(report.Skipped, SkippedPath{TenantID: id, Path: path, Reason: reason})
	s.logger.Warn("deleted-tenant reconcile left a path in place",
		slog.String("tenant_id", id),
		slog.String("path", path),
		slog.String("reason", reason))
}

// fullyReclaimed reports whether this node holds nothing more of a retired id:
// no candidate directory exists and the registry holds no barrier, record or
// slot assignment for it. Such an id needs no work, so a pass does not stop
// runtimes, release barriers or touch records for every retirement ever made.
func (s *TenantService) fullyReclaimed(ctx context.Context, id string, candidates []string) (bool, error) {
	for _, path := range candidates {
		if _, err := os.Lstat(path); err == nil {
			return false, nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return false, err
		}
	}
	held, err := s.registry.HasTenantState(ctx, id)
	if err != nil {
		return false, err
	}
	return !held, nil
}

// reasonAbsent marks a candidate that does not exist on disk; it is neither
// reclaimed nor skipped.
const reasonAbsent = "absent"

// removeResidue removes one candidate directory, or says why it did not. It
// returns "" when it removed the path, a per-id failure when this path could
// not be judged or removed, and an abort when the live set could not be read.
//
// path is the candidate AS RECORDED. It is validated before anything cleans
// it: "/srv/qrdb/x/.." cleans to "/srv/qrdb", a different directory that may
// well exist, so a dotted record is skipped rather than resolved.
func (s *TenantService) removeResidue(ctx context.Context, id, path string, g *residueGuards) (string, *ResidueReconcileError, error) {
	if validation.AbsoluteCanonicalPath(path) != nil {
		return SkipNotCanonical, nil, nil
	}
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return reasonAbsent, nil, nil
		}
		return "", &ResidueReconcileError{TenantID: id, Op: "remove", Path: path, Err: err}, nil
	}
	if overlapsAny(path, g.live) {
		return SkipOverlapsLiveTenant, nil, nil
	}
	if reservedConflict(path, g) {
		return SkipOverlapsReservedRoot, nil, nil
	}
	// Re-read the live set right before the destructive step: a tenant
	// created on a peer since the pass began may have taken this directory.
	fresh, err := s.liveTenantRootsStrict(ctx)
	if err != nil {
		return "", nil, &ResidueReconcileError{TenantID: id, Op: "list-tenants", Path: path, Err: err}
	}
	if overlapsAny(path, fresh) {
		return SkipOverlapsLiveTenant, nil, nil
	}
	if err := s.removeAll(path); err != nil {
		return "", &ResidueReconcileError{TenantID: id, Op: "remove", Path: path, Err: err}, nil
	}
	return "", nil, nil
}

// reservedConflict reports whether removing path would touch a volume-pool
// base or a reserved data root.
//
// A path that IS or CONTAINS a pool base or a reserved root is always refused.
// A path that lies INSIDE a reserved root is refused too, EXCEPT when it lies
// strictly inside a pool base: a pool may be carved out of a reserved root
// (the daemon's data directory), and every tenant's pool directory lies
// inside its pool base by construction -- refusing those would make a pool
// under a reserved root unreclaimable.
func reservedConflict(path string, g *residueGuards) bool {
	if containsAny(path, g.poolBases) || containsAny(path, g.reserved) {
		return true
	}
	return overlapsAny(path, g.reserved) && !strictlyInsideAny(path, g.poolBases)
}

// strictlyInsideAny reports whether path lies strictly inside any of roots.
func strictlyInsideAny(path string, roots []string) bool {
	for _, r := range roots {
		if isPathAncestor(filepath.Clean(r), path) {
			return true
		}
	}
	return false
}

// liveTenantRootsStrict returns every directory a live tenant claims on this
// node: the DataRoot on its record, and everything the registry attributes to
// it locally. Unlike ListTenants it does not skip a record it cannot decode --
// it fails: a feed into a destructive operation must fail closed, and an
// unreadable record is a live tenant whose directory is unknown.
func (s *TenantService) liveTenantRootsStrict(ctx context.Context) ([]string, error) {
	keys, err := s.store.List(ctx, metadataPrefix)
	if err != nil {
		return nil, &StoreError{Op: "list", Err: err}
	}
	var roots []string
	for _, key := range keys {
		data, err := s.store.Get(ctx, key)
		if err != nil {
			if isAbsent(err) {
				continue
			}
			return nil, &StoreError{Op: "get", Err: err}
		}
		var info TenantInfo
		if err := json.Unmarshal(data, &info); err != nil {
			return nil, &StoreError{Op: "decode " + key, Err: err}
		}
		id := strings.TrimPrefix(key, metadataPrefix)
		if info.DataRoot != "" {
			roots = append(roots, filepath.Clean(info.DataRoot))
		}
		local, err := s.registry.TenantDataRootCandidates(ctx, id)
		if err != nil {
			return nil, err
		}
		roots = append(roots, local...)
	}
	return roots, nil
}

// overlapsAny reports whether path is, contains or lies inside any of roots.
func overlapsAny(path string, roots []string) bool {
	for _, r := range roots {
		if dataRootsOverlap(path, filepath.Clean(r)) {
			return true
		}
	}
	return false
}

// containsAny reports whether path is, or is an ancestor of, any of roots. It
// is the pool-base check: a tenant's pool directory lies INSIDE a pool base
// by construction, so only a path at or above a base is refused.
func containsAny(path string, roots []string) bool {
	for _, r := range roots {
		clean := filepath.Clean(r)
		if path == clean || isPathAncestor(path, clean) {
			return true
		}
	}
	return false
}

// ReconcileDeletedTenants on a node without the seal subsystem has nothing on
// disk to reclaim: dedicated directories exist only for barrier-backed
// isolation. It still reports how many retired ids it examined, so the verb
// behaves the same on every node.
func (s *kvTenantService) ReconcileDeletedTenants(ctx context.Context) (*ReconcileReport, error) {
	ids, err := retiredIDs(ctx, s.store.List)
	if err != nil {
		return nil, &ResidueReconcileError{Op: "list-tombstones", Err: err}
	}
	return &ReconcileReport{Inspected: len(ids)}, nil
}
