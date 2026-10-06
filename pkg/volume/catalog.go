// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package volume

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/lock"
)

// Key prefixes for the replicated catalog, assignment and health-report
// records (docs/volume-pools/engineering.md#persistence). All three sit in
// the reserved "__sys:" namespace: replicated state with no node-local
// file, readable standalone (a single-node deployment still runs the same
// KVServicer/TransactionServicer path) and under Raft alike.
const (
	PoolKeyPrefix       = "__sys:volumepool/pools/"
	AssignmentKeyPrefix = "__sys:volumepool/assign/"
	NodeReportKeyPrefix = "__sys:volumepool/nodes/"
)

// PoolKey, AssignmentKey and NodeReportKey build the storage key for a
// Pool, Assignment or NodeReport record.
func PoolKey(name string) string           { return PoolKeyPrefix + name }
func AssignmentKey(tenantID string) string { return AssignmentKeyPrefix + tenantID }
func NodeReportKey(nodeID string) string   { return NodeReportKeyPrefix + nodeID }

// DefaultOvercommit is the overcommit ratio CreatePool uses when the
// request leaves it unset.
const DefaultOvercommit = 1.0

// DefaultReportInterval is how often a node is expected to refresh its
// NodeReport.
const DefaultReportInterval = 5 * time.Second

// DefaultLeaseTTL is how old a NodeReport may be before Catalog treats it
// as stale -- "no report" rather than the stale numbers it last held.
const DefaultLeaseTTL = 15 * time.Second

// maxCASRetries bounds every read-modify-write retry loop in this file,
// matching pkg/dek's keyring convention: every round has a winner under
// ordinary contention, so the limit is reached only under sustained races,
// and a caller that hits it retries at its own discretion.
const maxCASRetries = 16

// DefaultAssignmentPageSize and MaxAssignmentPageSize bound
// ListAssignments, following tenant.DeletedTenantsPage's paging contract.
const (
	DefaultAssignmentPageSize = 100
	MaxAssignmentPageSize     = 1000
)

// Catalog is the concrete Service and Allocator: the replicated pool
// catalog, tenant assignments and node health reports, backed by a
// KVServicer and a TransactionServicer. There is no node-local file: every
// record lives in the replicated system KV, so Catalog works identically
// in a single-node (standalone) deployment and under multi-node Raft.
type Catalog struct {
	kv       KVServicer
	tx       TransactionServicer
	registry Registry
	logger   *slog.Logger
	leaseTTL time.Duration
}

// Compile-time proof that Catalog satisfies both exported seams.
var (
	_ Service   = (*Catalog)(nil)
	_ Allocator = (*Catalog)(nil)
)

// CatalogOption configures a Catalog at construction time.
type CatalogOption func(*Catalog)

// WithCatalogLogger sets the Catalog's logger. A nil logger is ignored.
func WithCatalogLogger(l *slog.Logger) CatalogOption {
	return func(c *Catalog) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithLeaseTTL overrides DefaultLeaseTTL. A non-positive value is ignored.
func WithLeaseTTL(d time.Duration) CatalogOption {
	return func(c *Catalog) {
		if d > 0 {
			c.leaseTTL = d
		}
	}
}

// NewCatalog builds a Catalog over kv and tx, validating pool providers
// against registry. All three are required.
func NewCatalog(kv KVServicer, tx TransactionServicer, registry Registry, opts ...CatalogOption) (*Catalog, error) {
	if kv == nil {
		return nil, &InvalidCatalogDependencyError{Reason: "nil KVServicer"}
	}
	if tx == nil {
		return nil, &InvalidCatalogDependencyError{Reason: "nil TransactionServicer"}
	}
	if registry == nil {
		return nil, &InvalidCatalogDependencyError{Reason: "nil Registry"}
	}
	c := &Catalog{kv: kv, tx: tx, registry: registry, logger: slog.Default(), leaseTTL: DefaultLeaseTTL}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// ---------------------------------------------------------------------------
// Pool management (volume.Service)
// ---------------------------------------------------------------------------

// CreatePool adds a pool to the catalog.
func (c *Catalog) CreatePool(ctx context.Context, req CreatePoolRequest) (*Pool, error) {
	if req.Name == "" {
		return nil, &InvalidPoolRequestError{Reason: "empty name"}
	}
	provider := req.Provider
	if provider == "" {
		provider = "dir"
	}
	if _, err := c.registry.Lookup(provider); err != nil {
		return nil, err
	}
	if req.CapacityBytes < 0 {
		return nil, &InvalidPoolRequestError{Reason: "capacity must not be negative"}
	}
	overcommit := req.Overcommit
	if overcommit == 0 {
		overcommit = DefaultOvercommit
	}
	if overcommit < 1.0 {
		return nil, &InvalidPoolRequestError{Reason: "overcommit must be >= 1.0"}
	}

	now := time.Now()
	pool := Pool{
		Name:          req.Name,
		Provider:      provider,
		Description:   req.Description,
		CapacityBytes: req.CapacityBytes,
		Overcommit:    overcommit,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	data, err := json.Marshal(pool)
	if err != nil {
		return nil, &EncodeError{Err: err}
	}
	stored, err := c.kv.PutIfAbsent(ctx, PoolKey(req.Name), data)
	if err != nil {
		return nil, err
	}
	if !stored {
		return nil, &PoolAlreadyExistsError{Pool: req.Name}
	}
	return &pool, nil
}

// getPoolRaw reads and decodes the pool record at name, returning the
// decoded Pool and its raw bytes (needed by the CAS callers above it).
func (c *Catalog) getPoolRaw(ctx context.Context, name string) (*Pool, []byte, error) {
	raw, err := c.kv.Get(ctx, PoolKey(name))
	if err != nil {
		return nil, nil, err
	}
	if raw == nil {
		return nil, nil, &PoolNotFoundError{Pool: name}
	}
	var pool Pool
	if err := json.Unmarshal(raw, &pool); err != nil {
		return nil, nil, &CorruptRecordError{Key: PoolKey(name), Err: err}
	}
	return &pool, raw, nil
}

// UpdatePool changes a pool's description, capacity or overcommit. The
// provider is immutable. Lowering capacity below the current reservation
// is refused.
func (c *Catalog) UpdatePool(ctx context.Context, name string, req UpdatePoolRequest) (*Pool, error) {
	if req.CapacityBytes != nil && *req.CapacityBytes < 0 {
		return nil, &InvalidPoolRequestError{Reason: "capacity must not be negative"}
	}
	if req.Overcommit != nil && *req.Overcommit < 1.0 {
		return nil, &InvalidPoolRequestError{Reason: "overcommit must be >= 1.0"}
	}

	for attempt := 0; attempt < maxCASRetries; attempt++ {
		pool, raw, err := c.getPoolRaw(ctx, name)
		if err != nil {
			return nil, err
		}
		updated := *pool
		if req.Description != nil {
			updated.Description = *req.Description
		}
		if req.CapacityBytes != nil {
			if *req.CapacityBytes != 0 && *req.CapacityBytes < pool.ReservedBytes {
				return nil, &PoolCapacityExceededError{Pool: name, Requested: pool.ReservedBytes, Available: *req.CapacityBytes}
			}
			updated.CapacityBytes = *req.CapacityBytes
		}
		if req.Overcommit != nil {
			updated.Overcommit = *req.Overcommit
		}
		updated.UpdatedAt = time.Now()

		data, err := json.Marshal(updated)
		if err != nil {
			return nil, &EncodeError{Err: err}
		}
		swapped, err := c.kv.CompareAndSwap(ctx, PoolKey(name), raw, data)
		if err != nil {
			return nil, err
		}
		if swapped {
			return &updated, nil
		}
	}
	return nil, &ReservationConflictError{Pool: name}
}

// DeletePool removes a pool from the catalog. Refused while any tenant is
// assigned to it.
func (c *Catalog) DeletePool(ctx context.Context, name string) error {
	for attempt := 0; attempt < maxCASRetries; attempt++ {
		_, raw, err := c.getPoolRaw(ctx, name)
		if err != nil {
			return err
		}
		page, err := c.ListAssignments(ctx, name, "", DefaultAssignmentPageSize)
		if err != nil {
			return err
		}
		if len(page.Assignments) > 0 {
			ids := make([]string, len(page.Assignments))
			for i, a := range page.Assignments {
				ids[i] = a.TenantID
			}
			return &PoolInUseError{Pool: name, Tenants: ids}
		}
		// Invariant 9: a pool is never deleted while ANY record names it,
		// including one ListAssignments' best-effort scan could not even
		// parse enough to compare against name (a truly corrupt record,
		// not merely a D9 interim-shaped one -- ListAssignments already
		// includes those, since their Pool field decodes fine). Rather
		// than silently treat "could not tell" as "does not name this
		// pool" (what the best-effort scan above does, correctly, for
		// DISPLAY purposes), DeletePool fails closed: it cannot prove the
		// record does not name this pool, so it refuses.
		if unparseable, err := c.hasUnparseableAssignment(ctx); err != nil {
			return err
		} else if unparseable {
			return &PoolUnparseableAssignmentError{Pool: name}
		}
		deleted, err := c.kv.CompareAndDelete(ctx, PoolKey(name), raw)
		if err != nil {
			return err
		}
		if deleted {
			return nil
		}
	}
	return &ReservationConflictError{Pool: name}
}

// scanPools returns every Pool in the catalog, sorted by name. A record
// that fails to decode is skipped with a logged warning rather than
// failing the whole scan: one corrupt pool must not hide every other one
// from an operator trying to see the catalog.
func (c *Catalog) scanPools(ctx context.Context) ([]Pool, error) {
	raw, err := c.kv.Scan(ctx, PoolKeyPrefix)
	if err != nil {
		return nil, err
	}
	pools := make([]Pool, 0, len(raw))
	for key, data := range raw {
		var pool Pool
		if err := json.Unmarshal(data, &pool); err != nil {
			c.logger.Warn("volume: skipping corrupt pool record", slog.String("key", key), slog.String("error", err.Error()))
			continue
		}
		pools = append(pools, pool)
	}
	sort.Slice(pools, func(i, j int) bool { return pools[i].Name < pools[j].Name })
	return pools, nil
}

// ListPools lists every pool in the catalog with its aggregate view.
func (c *Catalog) ListPools(ctx context.Context) ([]PoolView, error) {
	pools, err := c.scanPools(ctx)
	if err != nil {
		return nil, err
	}
	reports, err := c.scanNodeReports(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]PoolView, 0, len(pools))
	for _, pool := range pools {
		tenantCount, err := c.tenantCount(ctx, pool.Name)
		if err != nil {
			return nil, err
		}
		now := time.Now()
		views = append(views, PoolView{
			Pool:        pool,
			UsedBytes:   usedBytesAcrossNodes(reports, pool.Name, now, c.leaseTTL),
			TenantCount: tenantCount,
			NodeCounts:  nodeHealthCounts(reports, pool.Name, now, c.leaseTTL),
		})
	}
	return views, nil
}

// GetPool inspects one pool: its aggregate view plus one row per node that
// has reported it.
func (c *Catalog) GetPool(ctx context.Context, name string) (*PoolDetail, error) {
	pool, _, err := c.getPoolRaw(ctx, name)
	if err != nil {
		return nil, err
	}
	tenantCount, err := c.tenantCount(ctx, name)
	if err != nil {
		return nil, err
	}
	reports, err := c.scanNodeReports(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	detail := &PoolDetail{
		PoolView: PoolView{
			Pool:        *pool,
			UsedBytes:   usedBytesAcrossNodes(reports, name, now, c.leaseTTL),
			TenantCount: tenantCount,
			NodeCounts:  nodeHealthCounts(reports, name, now, c.leaseTTL),
		},
		NodeReports: nodeReportRows(reports, name, now, c.leaseTTL),
	}
	return detail, nil
}

// tenantCount returns how many tenants are assigned to pool.
func (c *Catalog) tenantCount(ctx context.Context, pool string) (int, error) {
	n := 0
	raw, err := c.kv.Scan(ctx, AssignmentKeyPrefix)
	if err != nil {
		return 0, err
	}
	for key, data := range raw {
		var a Assignment
		if err := json.Unmarshal(data, &a); err != nil {
			c.logger.Warn("volume: skipping corrupt assignment record", slog.String("key", key), slog.String("error", err.Error()))
			continue
		}
		if a.Pool == pool {
			n++
		}
	}
	return n, nil
}

// hasUnparseableAssignment reports whether ANY assignment record in the
// catalog fails to decode at all. DeletePool's invariant-9 safety check:
// a D9 interim-shaped record ({pool, slot}) decodes fine (its Pool field
// matches the current Assignment shape, so tenantCount/ListAssignments
// already count it correctly) -- this catches the different, rarer case
// of a genuinely malformed record, which those best-effort scans skip
// with only a logged warning. DeletePool cannot prove such a record does
// not name the pool being deleted, so it must fail closed rather than
// silently proceed.
func (c *Catalog) hasUnparseableAssignment(ctx context.Context) (bool, error) {
	raw, err := c.kv.Scan(ctx, AssignmentKeyPrefix)
	if err != nil {
		return false, err
	}
	for key, data := range raw {
		var a Assignment
		if err := json.Unmarshal(data, &a); err != nil {
			c.logger.Warn("volume: assignment record could not be parsed; refusing any pool deletion until it is resolved",
				slog.String("key", key), slog.String("error", err.Error()))
			return true, nil
		}
	}
	return false, nil
}

// ---------------------------------------------------------------------------
// Node health reports
//
// A node's own report is written by Reporter (reporter.go), not by Catalog:
// it is lease-attached so the underlying engine deletes it automatically
// when the lease expires, exactly as cluster.Advertiser publishes a
// cluster.Member. Catalog only ever reads these records.
// ---------------------------------------------------------------------------

// scanNodeReports returns every NodeReport currently in the store. A
// genuinely dead node's report is simply absent once its lease has
// expired; the staleness check in nodeHealthCounts / nodeReportRows is
// defense in depth for the window before that sweep runs, or a report
// Reporter did not attach to a lease.
func (c *Catalog) scanNodeReports(ctx context.Context) ([]NodeReport, error) {
	raw, err := c.kv.Scan(ctx, NodeReportKeyPrefix)
	if err != nil {
		return nil, err
	}
	reports := make([]NodeReport, 0, len(raw))
	for key, data := range raw {
		var r NodeReport
		if err := json.Unmarshal(data, &r); err != nil {
			c.logger.Warn("volume: skipping corrupt node report", slog.String("key", key), slog.String("error", err.Error()))
			continue
		}
		reports = append(reports, r)
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].NodeID < reports[j].NodeID })
	return reports, nil
}

// nodeHealthCounts tallies the ready/locked/unavailable breakdown for pool
// across reports. Unmapped is always 0; see NodeHealthCounts.
func nodeHealthCounts(reports []NodeReport, pool string, now time.Time, ttl time.Duration) NodeHealthCounts {
	var counts NodeHealthCounts
	for _, r := range reports {
		onNode, ok := r.Pools[pool]
		if !ok || !onNode.Mapped {
			continue
		}
		if now.Sub(r.ReportedAt) > ttl {
			continue
		}
		switch onNode.Health.State {
		case StateReady:
			counts.Ready++
		case StateLocked:
			counts.Locked++
		default:
			counts.Unavailable++
		}
	}
	return counts
}

// usedBytesAcrossNodes approximates PoolView.UsedBytes from node health
// reports: the BUSIEST StateReady, non-stale node's measured usage
// (Usage.SizeBytes - Usage.FreeBytes) for pool. "Busiest," not summed or
// averaged, for the same reason effectiveCapacity takes the SMALLEST
// node's size rather than a total: under decision D1 (one volume per
// pool per node), each node's bytes are its own local volume, not a
// shared filesystem, so there is no single cluster-wide "total used" to
// sum to -- but the busiest node is the one closest to the capacity
// ceiling effectiveCapacity derives from the SMALLEST node, which is the
// operationally relevant number. This is a reporting approximation, not
// the storage-quota reconciler's own directory walk (quota.NewReconciler,
// not wired to this catalog): it is only as fresh as the last health
// report, and a negative FreeBytes/SizeBytes from a misbehaving provider
// is clamped to 0 rather than propagated.
func usedBytesAcrossNodes(reports []NodeReport, pool string, now time.Time, ttl time.Duration) int64 {
	var busiest int64
	for _, r := range reports {
		onNode, ok := r.Pools[pool]
		if !ok || !onNode.Mapped || onNode.Health.State != StateReady {
			continue
		}
		if now.Sub(r.ReportedAt) > ttl {
			continue
		}
		used := onNode.Usage.SizeBytes - onNode.Usage.FreeBytes
		if used < 0 {
			used = 0
		}
		if used > busiest {
			busiest = used
		}
	}
	return busiest
}

// nodeReportRows builds PoolDetail's per-node rows for pool.
func nodeReportRows(reports []NodeReport, pool string, now time.Time, ttl time.Duration) []NodeReportRow {
	rows := make([]NodeReportRow, 0, len(reports))
	for _, r := range reports {
		onNode, ok := r.Pools[pool]
		if !ok {
			continue
		}
		row := NodeReportRow{NodeID: r.NodeID, ReportedAt: r.ReportedAt}
		switch {
		case now.Sub(r.ReportedAt) > ttl:
			row.State = NodeStateNoReport
		case !onNode.Mapped:
			row.State = NodeStateUnmapped
		default:
			row.Detail = onNode.Health.Detail
			row.SizeBytes = onNode.Usage.SizeBytes
			row.FreeBytes = onNode.Usage.FreeBytes
			switch onNode.Health.State {
			case StateReady:
				row.State = NodeStateReady
			case StateLocked:
				row.State = NodeStateLocked
			default:
				row.State = NodeStateUnavailable
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// ---------------------------------------------------------------------------
// Assignments (volume.Service.ListAssignments)
// ---------------------------------------------------------------------------

// ListAssignments lists the tenants assigned to pool, paginated like
// tenant.DeletedTenantsPage: NextPageToken is opaque, empty on the last
// page, and resumes strictly after the tenant id it names. An empty pool
// lists assignments to every pool.
func (c *Catalog) ListAssignments(ctx context.Context, pool, pageToken string, pageSize int) (*AssignmentPage, error) {
	size, err := resolveAssignmentPageSize(pageSize)
	if err != nil {
		return nil, err
	}
	after, err := decodeAssignmentPageToken(pageToken)
	if err != nil {
		return nil, err
	}

	raw, err := c.kv.Scan(ctx, AssignmentKeyPrefix)
	if err != nil {
		return nil, err
	}
	all := make([]Assignment, 0, len(raw))
	for key, data := range raw {
		var a Assignment
		if err := json.Unmarshal(data, &a); err != nil {
			c.logger.Warn("volume: skipping corrupt assignment record", slog.String("key", key), slog.String("error", err.Error()))
			continue
		}
		if pool != "" && a.Pool != pool {
			continue
		}
		all = append(all, a)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].TenantID < all[j].TenantID })

	start := sort.Search(len(all), func(i int) bool { return all[i].TenantID > after })
	page := &AssignmentPage{Assignments: make([]Assignment, 0, size)}
	i := start
	for ; i < len(all) && len(page.Assignments) < size; i++ {
		page.Assignments = append(page.Assignments, all[i])
	}
	if i < len(all) && len(page.Assignments) > 0 {
		page.NextPageToken = encodeAssignmentPageToken(page.Assignments[len(page.Assignments)-1].TenantID)
	}
	return page, nil
}

func resolveAssignmentPageSize(size int) (int, error) {
	switch {
	case size < 0:
		return 0, &InvalidPageRequestError{Field: "page_size", Reason: "must not be negative"}
	case size == 0:
		return DefaultAssignmentPageSize, nil
	case size > MaxAssignmentPageSize:
		return MaxAssignmentPageSize, nil
	default:
		return size, nil
	}
}

func encodeAssignmentPageToken(lastTenantID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(lastTenantID))
}

func decodeAssignmentPageToken(token string) (string, error) {
	if token == "" {
		return "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return "", &InvalidPageRequestError{Field: "page_token", Reason: "not a valid page token for this listing"}
	}
	return string(raw), nil
}

// ---------------------------------------------------------------------------
// Moves (Phase 2, decision D5)
// ---------------------------------------------------------------------------

// MoveTenant is not implemented: tenant moves are Phase 2.
func (c *Catalog) MoveTenant(_ context.Context, _, _ string) (*Move, error) {
	return nil, &MoveNotImplementedError{}
}

// GetMove is not implemented: tenant moves are Phase 2.
func (c *Catalog) GetMove(_ context.Context, _ string) (*Move, error) {
	return nil, &MoveNotImplementedError{}
}

// ---------------------------------------------------------------------------
// Allocator: Reserve / Resolve / Release
// ---------------------------------------------------------------------------

// ValidateAssignmentPath reports whether path is one clean path component:
// non-empty, with no path separator, and not "." or ".." (invariant 2).
func ValidateAssignmentPath(tenantID, path string) error {
	if path == "" || path != filepath.Base(path) || path == "." || path == ".." ||
		strings.ContainsRune(path, '/') || strings.ContainsRune(path, filepath.Separator) {
		return &InvalidAssignmentPathError{TenantID: tenantID, Path: path}
	}
	return nil
}

// effectiveCapacity returns pool's capacity for Reserve's enforcement
// check: pool.CapacityBytes directly when the operator set one, or
// decision D4's derivation when not (CreatePool/UpdatePool never store a
// genuinely-unlimited pool -- 0 always means "derive," never "no limit").
// D4: the smallest StateReady node's reported Usage.SizeBytes for this
// pool, within the lease TTL -- the same staleness rule
// nodeHealthCounts/nodeReportRows use, so a dead node's last report
// cannot inflate (or shrink) the derived number forever. Returns 0 (no
// cap enforced yet) only when there is nothing to derive from: no node
// has reported this pool as StateReady at all, the transient bootstrap
// window right after CreatePool, before any node's Reporter has run a
// cycle.
func (c *Catalog) effectiveCapacity(ctx context.Context, pool *Pool) (int64, error) {
	if pool.CapacityBytes > 0 {
		return pool.CapacityBytes, nil
	}
	reports, err := c.scanNodeReports(ctx)
	if err != nil {
		return 0, err
	}
	now := time.Now()
	smallest := int64(-1)
	for _, r := range reports {
		onNode, ok := r.Pools[pool.Name]
		if !ok || !onNode.Mapped || onNode.Health.State != StateReady {
			continue
		}
		if now.Sub(r.ReportedAt) > c.leaseTTL {
			continue
		}
		if smallest == -1 || onNode.Usage.SizeBytes < smallest {
			smallest = onNode.Usage.SizeBytes
		}
	}
	if smallest == -1 {
		return 0, nil
	}
	return smallest, nil
}

// Reserve implements Allocator.
func (c *Catalog) Reserve(ctx context.Context, tenantID, poolName, path string, reserveBytes int64) (*Assignment, error) {
	if tenantID == "" {
		return nil, &InvalidCatalogDependencyError{Reason: "empty tenant id"}
	}
	if err := ValidateAssignmentPath(tenantID, path); err != nil {
		return nil, err
	}

	for attempt := 0; attempt < maxCASRetries; attempt++ {
		// Invariant 3: a tenant has at most one assignment. Resolve first
		// so a retried create is idempotent rather than erroring or
		// double-reserving.
		existing, err := c.Resolve(ctx, tenantID)
		if err == nil {
			if existing.Pool != poolName {
				return nil, &TenantAlreadyAssignedError{TenantID: tenantID, CurrentPool: existing.Pool, RequestedPool: poolName}
			}
			return existing, nil
		}
		var notFound *AssignmentNotFoundError
		if !isAssignmentNotFound(err, &notFound) {
			return nil, err
		}

		pool, poolRaw, err := c.getPoolRaw(ctx, poolName)
		if err != nil {
			return nil, err
		}
		overcommit := pool.Overcommit
		if overcommit == 0 {
			overcommit = DefaultOvercommit
		}
		// Decision D4: a pool created with no explicit capacity derives
		// one from the smallest healthy node's reported size, rather than
		// being unconstrained forever. effectiveCapacity returns 0 (no
		// cap enforced) only when there is nothing yet to derive from --
		// an explicit capacity of 0 is refused by CreatePool/UpdatePool,
		// so a stored CapacityBytes of 0 always means "derive," never
		// "unlimited by the operator's own choice."
		capacity, err := c.effectiveCapacity(ctx, pool)
		if err != nil {
			return nil, err
		}
		if capacity > 0 {
			limit := int64(float64(capacity) * overcommit)
			available := limit - pool.ReservedBytes
			if reserveBytes > available {
				return nil, &PoolCapacityExceededError{Pool: poolName, Requested: reserveBytes, Available: available}
			}
		}

		updatedPool := *pool
		updatedPool.ReservedBytes += reserveBytes
		updatedPool.UpdatedAt = time.Now()
		poolData, err := json.Marshal(updatedPool)
		if err != nil {
			return nil, &EncodeError{Err: err}
		}

		assignment := Assignment{TenantID: tenantID, Pool: poolName, Path: path, ReservedBytes: reserveBytes}
		assignData, err := json.Marshal(assignment)
		if err != nil {
			return nil, &EncodeError{Err: err}
		}

		resp, err := c.tx.Execute(ctx,
			[]lock.Compare{
				{Key: []byte(PoolKey(poolName)), Target: lock.CompareValue, Result: lock.CompareEqual, Value: poolRaw},
				{Key: []byte(AssignmentKey(tenantID)), Target: lock.CompareVersion, Result: lock.CompareEqual, Version: 0},
			},
			[]lock.Op{
				{Type: lock.OpPut, Key: []byte(PoolKey(poolName)), Value: poolData},
				{Type: lock.OpPut, Key: []byte(AssignmentKey(tenantID)), Value: assignData},
			},
			nil,
		)
		if err != nil {
			return nil, err
		}
		if resp.Succeeded {
			return &assignment, nil
		}
		// Lost the race: either the pool changed or another writer beat
		// us to the assignment. Loop and re-read both.
	}
	return nil, &ReservationConflictError{Pool: poolName}
}

// Resolve implements Allocator.
//
// A decoded record whose Path fails ValidateAssignmentPath is refused
// with *InvalidAssignmentPathError rather than returned: this is where a
// pre-catalog record (the interim fix's {pool, slot} shape, which has no
// Path field at all and so decodes with Path == "") is refused per
// decision D9, and the fail-closed defense invariant 2 asks for on every
// use, not only on write.
func (c *Catalog) Resolve(ctx context.Context, tenantID string) (*Assignment, error) {
	raw, err := c.kv.Get(ctx, AssignmentKey(tenantID))
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, &AssignmentNotFoundError{TenantID: tenantID}
	}
	var a Assignment
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, &CorruptRecordError{Key: AssignmentKey(tenantID), Err: err}
	}
	if err := ValidateAssignmentPath(tenantID, a.Path); err != nil {
		return nil, err
	}
	return &a, nil
}

// ListAll implements Allocator.
func (c *Catalog) ListAll(ctx context.Context) ([]Assignment, error) {
	var all []Assignment
	token := ""
	for {
		page, err := c.ListAssignments(ctx, "", token, MaxAssignmentPageSize)
		if err != nil {
			return nil, err
		}
		all = append(all, page.Assignments...)
		if page.NextPageToken == "" {
			return all, nil
		}
		token = page.NextPageToken
	}
}

// Release implements Allocator.
func (c *Catalog) Release(ctx context.Context, tenantID string) error {
	for attempt := 0; attempt < maxCASRetries; attempt++ {
		assignRaw, err := c.kv.Get(ctx, AssignmentKey(tenantID))
		if err != nil {
			return err
		}
		if assignRaw == nil {
			return nil // invariant: releasing an unassigned tenant is a no-op
		}
		var assignment Assignment
		if err := json.Unmarshal(assignRaw, &assignment); err != nil {
			return &CorruptRecordError{Key: AssignmentKey(tenantID), Err: err}
		}

		// A D9 interim-shaped record ({pool, slot}, no Path field) never
		// reserved bytes against this catalog's pool capacity tracking --
		// it predates the catalog entirely -- so there is no pool to
		// credit back. Delete the record outright per D9(a): "refuse them
		// like path records... the tenant is deleted and re-created."
		// DestroyTenant/reconcile depend on this: without it, an operator
		// could never finish deleting a tenant stuck with one of these,
		// since Resolve (and therefore RequireInsideLocalPool) always
		// refuses it, and this Release path previously assumed every
		// decoded record had a real Pool to look up.
		if ValidateAssignmentPath(tenantID, assignment.Path) != nil {
			deleted, err := c.kv.CompareAndDelete(ctx, AssignmentKey(tenantID), assignRaw)
			if err != nil {
				return err
			}
			if deleted {
				return nil
			}
			continue
		}

		pool, poolRaw, err := c.getPoolRaw(ctx, assignment.Pool)
		if err != nil {
			return err
		}
		updatedPool := *pool
		updatedPool.ReservedBytes -= assignment.ReservedBytes
		if updatedPool.ReservedBytes < 0 {
			updatedPool.ReservedBytes = 0
		}
		updatedPool.UpdatedAt = time.Now()
		poolData, err := json.Marshal(updatedPool)
		if err != nil {
			return &EncodeError{Err: err}
		}

		resp, err := c.tx.Execute(ctx,
			[]lock.Compare{
				{Key: []byte(PoolKey(assignment.Pool)), Target: lock.CompareValue, Result: lock.CompareEqual, Value: poolRaw},
				{Key: []byte(AssignmentKey(tenantID)), Target: lock.CompareValue, Result: lock.CompareEqual, Value: assignRaw},
			},
			[]lock.Op{
				{Type: lock.OpPut, Key: []byte(PoolKey(assignment.Pool)), Value: poolData},
				{Type: lock.OpDelete, Key: []byte(AssignmentKey(tenantID))},
			},
			nil,
		)
		if err != nil {
			return err
		}
		if resp.Succeeded {
			return nil
		}
	}
	return &ReservationConflictError{Pool: tenantID}
}

// AdjustReservation implements Allocator.
func (c *Catalog) AdjustReservation(ctx context.Context, tenantID string, newBytes int64) (*Assignment, error) {
	for attempt := 0; attempt < maxCASRetries; attempt++ {
		assignRaw, err := c.kv.Get(ctx, AssignmentKey(tenantID))
		if err != nil {
			return nil, err
		}
		if assignRaw == nil {
			return nil, &AssignmentNotFoundError{TenantID: tenantID}
		}
		var assignment Assignment
		if err := json.Unmarshal(assignRaw, &assignment); err != nil {
			return nil, &CorruptRecordError{Key: AssignmentKey(tenantID), Err: err}
		}
		if err := ValidateAssignmentPath(tenantID, assignment.Path); err != nil {
			return nil, err
		}
		if assignment.ReservedBytes == newBytes {
			return &assignment, nil
		}

		pool, poolRaw, err := c.getPoolRaw(ctx, assignment.Pool)
		if err != nil {
			return nil, err
		}
		delta := newBytes - assignment.ReservedBytes
		if delta > 0 {
			// Only an INCREASE is checked against capacity, exactly as
			// Reserve checks a brand new reservation; a decrease always
			// fits (it can only free room).
			capacity, err := c.effectiveCapacity(ctx, pool)
			if err != nil {
				return nil, err
			}
			overcommit := pool.Overcommit
			if overcommit == 0 {
				overcommit = DefaultOvercommit
			}
			if capacity > 0 {
				limit := int64(float64(capacity) * overcommit)
				available := limit - pool.ReservedBytes
				if delta > available {
					return nil, &PoolCapacityExceededError{Pool: assignment.Pool, Requested: delta, Available: available}
				}
			}
		}

		updatedPool := *pool
		updatedPool.ReservedBytes += delta
		if updatedPool.ReservedBytes < 0 {
			updatedPool.ReservedBytes = 0
		}
		updatedPool.UpdatedAt = time.Now()
		poolData, err := json.Marshal(updatedPool)
		if err != nil {
			return nil, &EncodeError{Err: err}
		}

		updatedAssignment := assignment
		updatedAssignment.ReservedBytes = newBytes
		assignData, err := json.Marshal(updatedAssignment)
		if err != nil {
			return nil, &EncodeError{Err: err}
		}

		resp, err := c.tx.Execute(ctx,
			[]lock.Compare{
				{Key: []byte(PoolKey(assignment.Pool)), Target: lock.CompareValue, Result: lock.CompareEqual, Value: poolRaw},
				{Key: []byte(AssignmentKey(tenantID)), Target: lock.CompareValue, Result: lock.CompareEqual, Value: assignRaw},
			},
			[]lock.Op{
				{Type: lock.OpPut, Key: []byte(PoolKey(assignment.Pool)), Value: poolData},
				{Type: lock.OpPut, Key: []byte(AssignmentKey(tenantID)), Value: assignData},
			},
			nil,
		)
		if err != nil {
			return nil, err
		}
		if resp.Succeeded {
			return &updatedAssignment, nil
		}
	}
	return nil, &ReservationConflictError{Pool: tenantID}
}

// isAssignmentNotFound reports whether err is an *AssignmentNotFoundError,
// assigning it through target on success. A small local helper rather than
// a bare errors.As call at each use so the zero-value *target reset on a
// non-match is explicit.
func isAssignmentNotFound(err error, target **AssignmentNotFoundError) bool {
	return errors.As(err, target)
}
