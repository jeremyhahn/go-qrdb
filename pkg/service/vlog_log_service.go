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

//go:build qrdb_vlog

package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// logService is the LogService over the tenant's group: writes are
// OpVerifiableLog proposals, reads are vlog.Request lookups.
type logService struct {
	route   *logRoute
	signers vlog.HeadSignerProvider
}

var _ LogService = (*logService)(nil)

// NewLogService returns the LogService cfg composes. The resolver and the
// database lookup are required; a nil Signers is the software HeadSignerProvider, reading each
// log's k/<version> record through the log's group.
func NewLogService(cfg LogServiceConfig) (LogService, error) {
	if cfg.Resolver == nil {
		return nil, &SubsystemNotConfiguredError{Subsystem: "vlog shard resolver"}
	}
	if cfg.Databases == nil {
		return nil, &SubsystemNotConfiguredError{Subsystem: "vlog database lookup"}
	}
	route := &logRoute{resolver: cfg.Resolver, audit: cfg.AuditResolver, databases: cfg.Databases, stores: make(map[dbStoreKey]*databaseLogStore), barriers: make(map[string]*barrierLogStore)}
	signers := cfg.Signers
	if signers == nil {
		signers = vlog.NewSoftwareHeadSignerProvider(&logKeyReader{route: route})
	}
	return &logService{route: route, signers: signers}, nil
}

// logKeyReader is the vlog.SigningKeyReader the software provider reads a
// log's key through: a linearizable read of the group that holds the log.
type logKeyReader struct {
	route *logRoute
}

var _ vlog.SigningKeyReader = (*logKeyReader)(nil)

// ReadSigningKey reads id's k/<version> record.
func (r *logKeyReader) ReadSigningKey(ctx context.Context, id vlog.LogID, version uint32) (*vlog.SigningKey, error) {
	return logRead[*vlog.SigningKey](ctx, r.route, id, vlog.KeyRequest{ID: id, Version: version})
}

// logRefusalErrors rebuild the typed vlog error of each log-refusal reason
// from the log the command named and the tree size the refusal carries.
// protocol.DecodeLogRefusal accepts no other reason.
var logRefusalErrors = map[protocol.LogRefusalReason]func(id vlog.LogID, size uint64) error{
	protocol.LogRefusalNotFound:         func(id vlog.LogID, _ uint64) error { return &vlog.LogNotFoundError{ID: id} },
	protocol.LogRefusalExists:           func(id vlog.LogID, _ uint64) error { return &vlog.LogExistsError{ID: id} },
	protocol.LogRefusalCheckpointExists: func(_ vlog.LogID, size uint64) error { return &vlog.CheckpointExistsError{Size: size} },
	protocol.LogRefusalExpireNotAllowed: func(id vlog.LogID, _ uint64) error { return &vlog.ExpireNotAllowedError{ID: id} },
}

// logID confines ref to the caller's tenant, validates the name and refuses a
// database whose isolation the log registry cannot honour.
func (s *logService) logID(ctx context.Context, ref LogRef, op string) (vlog.LogID, error) {
	own, err := tenant.Confine(ctx, ref.TenantID, op)
	if err != nil {
		return vlog.LogID{}, err
	}
	id := vlog.LogID{TenantID: routingTenant(own), Database: ref.Database, Log: ref.Log}
	if err := id.Validate(); err != nil {
		return vlog.LogID{}, err
	}
	return id, nil
}

// resolve is the logRoute's resolution of tenantID's database name to the
// target its logs are applied in and read from, with the database that one
// lookup found: nil for a name no database holds and for the audit log a
// separate audit resolver serves. A caller fences to that database's
// incarnation, never to a second lookup's (which a drop may already have
// overtaken).
func (r *logRoute) resolve(ctx context.Context, id vlog.LogID) (logTarget, *database.Database, error) {
	tenantID, name := id.TenantID, id.Database
	if isAuditLog(id) && r.audit != nil {
		target, err := r.auditGroup(ctx, tenantID)
		return target, nil, err
	}
	db, err := r.databases.GetOwned(name, tenantID)
	if err != nil {
		var notFound *database.NotFoundError
		if !errors.As(err, &notFound) {
			return nil, nil, err
		}
		target, err := r.raft(ctx, tenantID)
		return target, nil, err
	}
	if db.BarrierEnabled() {
		cfg := db.Config()
		if !cfg.BarrierOnDataPath() {
			return nil, nil, &database.BarrierOffDataPathError{Name: name, ServedBy: database.ServedByRaftGroup}
		}
		return r.store(tenantID, name, db), db, nil
	}
	target, err := r.raft(ctx, tenantID)
	return target, db, err
}

// auditGroup returns the group the replicated audit logger writes tenantID's
// audit trail to. The reserved system database, the audit instance's home,
// is read and written there, so the audit reader and writer always use one
// group: for a barrier tenant that is the system shard, as on main.
func (r *logRoute) auditGroup(ctx context.Context, tenantID string) (logTarget, error) {
	host, shardID, err := r.audit.ResolveAuditShard(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	return raftTarget{host: host, shardID: shardID}, nil
}

// raft resolves tenantID's group as a raftTarget.
func (r *logRoute) raft(ctx context.Context, tenantID string) (logTarget, error) {
	host, shardID, err := r.resolver.ResolveAuditShard(ctx, tenantID)
	if err != nil {
		var unavailable *BarrierStoreUnavailableError
		if src, ok := r.resolver.(LogBarrierStoreSource); ok && errors.As(err, &unavailable) {
			return r.barrierStore(ctx, src, tenantID)
		}
		return nil, err
	}
	return raftTarget{host: host, shardID: shardID}, nil
}

// barrierStore returns the barrierLogStore of barrier tenant tenantID on a
// node no home group serves it on, keyed by the barrier instance src holds
// for it now: a recycled or restored barrier gets a fresh store (and chain),
// and the old one is forgotten.
func (r *logRoute) barrierStore(ctx context.Context, src LogBarrierStoreSource, tenantID string) (logTarget, error) {
	barrier, kv, err := src.TenantLogStore(ctx, tenantID)
	if err != nil {
		if isSealedError(err) {
			return nil, tenant.ErrTenantSealed
		}
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.barriers[tenantID]; ok && s.barrier == barrier {
		return s, nil
	}
	admit, _ := src.(LogStoreAdmission)
	s, err := newBarrierLogStore(tenantID, barrier, kv, admit, src)
	if err != nil {
		delete(r.barriers, tenantID)
		return nil, err
	}
	r.barriers[tenantID] = s
	return s, nil
}

// store returns the databaseLogStore of tenantID's barrier database db,
// keyed by its incarnation: a database dropped and created again under the
// name gets a fresh store, and the old one is forgotten.
func (r *logRoute) store(tenantID, name string, db *database.Database) *databaseLogStore {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := dbStoreKey{tenant: tenantID, name: name, incarnation: db.Incarnation()}
	if s, ok := r.stores[key]; ok && s.db == db {
		return s
	}
	for k := range r.stores {
		if k.tenant == tenantID && k.name == name {
			delete(r.stores, k)
		}
	}
	s := newDatabaseLogStore(tenantID, db)
	r.stores[key] = s
	return s
}

// logRoute resolves where a log in tenantID's database is applied and read:
// a barrier-enabled database's logs live in its own barrier store, under the
// database's key (databaseLogStore); a legacy raft-backend barrier record is
// refused with BarrierOffDataPathError. Every other log is in the tenant's
// Raft group (a barrier tenant's home group, else the system shard). A name
// with no database record carries no isolation of its own and routes with
// its tenant, the fall-through the tenant resolver gives a tenant without
// metadata; a failed lookup refuses.
type logRoute struct {
	resolver  AuditShardResolver
	audit     AuditShardResolver
	databases LogDatabaseLookup

	mu       sync.Mutex
	stores   map[dbStoreKey]*databaseLogStore
	barriers map[string]*barrierLogStore

	// fences writes a database's log marker where it was never written
	// (SetLogFenceInitializer); nil writes none. inited holds every
	// incarnation it has answered for, so it is asked once per incarnation.
	fences atomic.Pointer[logFenceInitializerRef]
	inited sync.Map // dbStoreKey -> struct{}
}

// logFenceInitializerRef boxes a LogFenceInitializer for an atomic pointer.
type logFenceInitializerRef struct{ f LogFenceInitializer }

// SetLogFenceInitializer sets the LogFenceInitializer every fenced log write
// is admitted through; nil removes it. The composer sets it once the
// keyspace reclaimer, built after the log service, exists.
func (s *logService) SetLogFenceInitializer(f LogFenceInitializer) {
	if f == nil {
		s.route.fences.Store(nil)
		return
	}
	s.route.fences.Store(&logFenceInitializerRef{f: f})
}

// initFence has incarnation of tenantID's database name written as its log
// marker when the marker was never written: a database created before a
// barrier tenant's logs were fenced in its home group would otherwise
// refuse every log write, its guard comparing the incarnation against an
// absent marker. It is incarnation-safe: incarnation is the catalog's live
// one, and a marker is absent only while no drop or create of the name has
// run since, each of which writes it (a drop tombstones even an absent
// marker), so a stale incarnation finds the marker present and writes
// nothing.
func (r *logRoute) initFence(ctx context.Context, tenantID, name, incarnation string) error {
	ref := r.fences.Load()
	if ref == nil || incarnation == "" {
		return nil
	}
	key := dbStoreKey{tenant: tenantID, name: name, incarnation: incarnation}
	if _, done := r.inited.Load(key); done {
		return nil
	}
	if err := ref.f.InitDatabaseLogFence(ctx, tenantID, name, incarnation); err != nil {
		return err
	}
	r.inited.Store(key, struct{}{})
	return nil
}

// dbStoreKey names one incarnation of a tenant's database.
type dbStoreKey struct{ tenant, name, incarnation string }

// logTarget is where a log's commands are applied and its reads answered.
type logTarget interface {
	propose(ctx context.Context, cmd []byte) (sm.Result, error)
	read(ctx context.Context, req vlog.Request) (interface{}, error)
	featureEnabled(f feature.Feature) bool
	// serving refuses a target that cannot serve now (a sealed barrier)
	// with the error its propose and read answer; nil when it serves. It is
	// checked before a signing key is minted, and propose checks again.
	serving() error
}

// raftTarget is a tenant's Raft group.
type raftTarget struct {
	host    Host
	shardID uint64
}

func (t raftTarget) propose(ctx context.Context, cmd []byte) (sm.Result, error) {
	result, err := propose(ctx, t.host, t.shardID, cmd)
	if err != nil {
		return sm.Result{}, translateProposeError(err)
	}
	return result, nil
}

func (t raftTarget) read(ctx context.Context, req vlog.Request) (interface{}, error) {
	result, err := t.host.SyncRead(ctx, t.shardID, req)
	if err != nil {
		var nle *quicraft.NotLeaderError
		if errors.As(err, &nle) {
			return nil, &NotLeaderError{ShardID: nle.ShardID, LeaderID: nle.LeaderID, LeaderAddress: nle.LeaderAddress}
		}
		return nil, &ReadError{Err: err}
	}
	return result, nil
}

// serving reports nil: the group's own apply refuses a sealed tenant.
func (t raftTarget) serving() error { return nil }

func (t raftTarget) featureEnabled(f feature.Feature) bool {
	if gated, ok := t.host.(FeatureGatedHost); ok {
		return gated.FeatureEnabled(t.shardID, f)
	}
	return true
}

// logFeatures gates the sub-types every replica that implements verifiable
// logs applies; logKeyFeatures adds the head-signing-key wire a create and a
// key rotation carry, which an older replica refuses.
var (
	logFeatures    = []feature.Feature{featuregate.VerifiableLog}
	logKeyFeatures = []feature.Feature{featuregate.VerifiableLog, featuregate.VerifiableLogKeys}
	// logIncarnationFeatures gates a key rotation of a log whose descriptor
	// records its database's incarnation: a replica without
	// featuregate.VerifiableLogDrop decodes the descriptor without the field
	// and would persist it dropped. A create on such a group records no
	// incarnation instead (CreateLog), so the log is legacy and belongs to
	// the database holding its name (vlog.IncarnationRequest).
	logIncarnationFeatures = []feature.Feature{featuregate.VerifiableLog, featuregate.VerifiableLogKeys, featuregate.VerifiableLogDrop}
)

// propose sends one encoded OpVerifiableLog command to the tenant's group,
// refused while any of features is not active there, and returns the apply's
// value.
func (s *logService) propose(ctx context.Context, id vlog.LogID, features []feature.Feature, encode func() ([]byte, error)) (uint64, error) {
	g, err := s.admit(ctx, id, features)
	if err != nil {
		return 0, err
	}
	return g.propose(ctx, id, encode)
}

// logGroup is the target a log write has been admitted to.
//
// incarnation is the incarnation of the database the log's name resolved to
// at admission, empty for a name no database holds. guard, when set, is that
// incarnation's drop fence (DropFenceGuard): every command proposed through
// g carries it, so the state machine refuses the write on every replica once
// the database's drop has fenced it, and a write admitted before a drop
// stays refused after the name is created again, whatever it waited on in
// between (a signer provider minting its key, a slow proposal).
type logGroup struct {
	target      logTarget
	incarnation string
	fence       logFence
}

// logFence fences one log write to the database incarnation it was admitted
// under. The zero logFence fences nothing: a log whose name no droppable
// database holds, the audit instance, a reclamation.
//
//   - guard is the incarnation's drop fence (DropFenceGuard), carried inside
//     the Raft command (OpGuarded), so every replica refuses the write once
//     the drop's tombstone (FenceDatabaseKeyspace) has landed in the group
//     -- the tenant's group or the system shard, which holds the marker of
//     every database whose logs it holds (docs/plans/1613-verifiable-log.md).
//   - live re-resolves the database and refuses the write unless the
//     incarnation is still the live one. A standalone barrier store checks
//     it under the lock its apply and the drop's reclamation both hold, so a
//     write admitted before a drop either applies before the reclamation
//     deletes it or is refused after.
type logFence struct {
	guard *WriteGuard
	live  func() error
}

// fencedLogTarget is a logTarget that applies a logFence with the write.
type fencedLogTarget interface {
	proposeFenced(ctx context.Context, cmd []byte, fence logFence) (sm.Result, error)
}

// admit resolves the tenant's target and refuses the write while any of
// features is not active there. A write that mints a key admits first, so a
// refused tenant never has a key created for it.
func (s *logService) admit(ctx context.Context, id vlog.LogID, features []feature.Feature) (logGroup, error) {
	tenantID := id.TenantID
	target, db, err := s.route.resolve(ctx, id)
	if err != nil {
		return logGroup{}, err
	}
	for _, f := range features {
		if !target.featureEnabled(f) {
			var shardID uint64
			if rt, ok := target.(raftTarget); ok {
				shardID = rt.shardID
			}
			return logGroup{}, &LogFeatureNotActiveError{TenantID: tenantID, ShardID: shardID, Feature: f}
		}
	}
	if err := target.serving(); err != nil {
		return logGroup{}, err
	}
	g := logGroup{target: target}
	if !s.route.fenced(id) {
		return g, nil
	}
	// The incarnation is the one resolve found the target by, in the same
	// lookup: every command proposed through g is fenced to it, so a write
	// that waits past a drop (a signer minting its key) is refused even once
	// the name is created again. A name no database holds is fenced to
	// that: its marker absent (a drop's tombstone outlives the drop) and no
	// database holding the name, so a write admitted as a drop finished is
	// refused rather than outliving the drop that reclaimed the name's logs.
	held := db != nil
	if held {
		g.incarnation = db.Incarnation()
		if err := s.route.initFence(ctx, tenantID, id.Database, g.incarnation); err != nil {
			return logGroup{}, err
		}
	}
	if g.fence, err = s.route.fence(tenantID, id.Database, g.incarnation, held); err != nil {
		return logGroup{}, err
	}
	return g, nil
}

// fenced reports whether a write to id is fenced to its database's
// incarnation: every log of a named database but the audit instance, which
// lives on the system shard with the reserved system database, and the
// default database's, which cannot be dropped (database.CheckDroppable).
func (r *logRoute) fenced(id vlog.LogID) bool {
	if id.TenantID == "" || id.Database == "" || id.Database == tenant.DefaultDatabase {
		return false
	}
	return !isAuditLog(id) || r.audit == nil
}

// fence returns the logFence of incarnation of tenantID's database name;
// held=false fences to no database holding the name (incarnation "").
func (r *logRoute) fence(tenantID, name, incarnation string, held bool) (logFence, error) {
	guard, err := DropFenceGuard(tenantID, name, incarnation)
	if err != nil {
		return logFence{}, err
	}
	return logFence{guard: &guard, live: func() error {
		return r.liveIncarnation(tenantID, name, incarnation, held)
	}}, nil
}

// liveIncarnation refuses with *database.DroppingError, as not stored,
// unless incarnation is the live incarnation of tenantID's database name: a
// database being dropped, dropped, or created again under the name since.
// held=false admits only while no database holds the name.
func (r *logRoute) liveIncarnation(tenantID, name, incarnation string, held bool) error {
	db, err := r.databases.GetOwned(name, tenantID)
	if err == nil && held && db.Incarnation() == incarnation {
		return nil
	}
	_, notFound := errors.AsType[*database.NotFoundError](err)
	if notFound && !held {
		return nil
	}
	if _, dropping := errors.AsType[*database.DroppingError](err); err != nil && !notFound && !dropping {
		return err
	}
	return markNotStored(&database.DroppingError{Name: name, TenantID: tenantID})
}

// proposeFenced proposes cmd wrapped in fence's drop-fence guard, which the
// group's state machine checks in the same command (OpGuarded); a refusal
// comes back as the result checkResultError reads as *database.DroppingError.
func (t raftTarget) proposeFenced(ctx context.Context, cmd []byte, fence logFence) (sm.Result, error) {
	if fence.guard != nil {
		guarded, err := fence.guard.wrap(cmd)
		if err != nil {
			return sm.Result{}, err
		}
		cmd = guarded
	}
	return t.propose(ctx, cmd)
}

// propose sends one encoded OpVerifiableLog command for the log id to g and
// returns the apply's value.
func (g logGroup) propose(ctx context.Context, id vlog.LogID, encode func() ([]byte, error)) (uint64, error) {
	cmd, err := encode()
	if err != nil {
		return 0, &EncodeRequestError{Err: err}
	}
	result, err := g.proposeCmd(ctx, cmd)
	if err != nil {
		return 0, err
	}
	// Every log command names one log, id, so a log refusal is answered with
	// its typed vlog error naming that log: a not-found one with the error a
	// read of the log returns.
	if reason, size, _, refused := protocol.DecodeLogRefusal(result.Data); refused {
		return 0, logRefusalErrors[reason](id, size)
	}
	if resultErr := checkResultError(result); resultErr != nil {
		return 0, &ProposeError{Err: resultErr}
	}
	return result.Value, nil
}

// proposeCmd proposes cmd to g's target under g's fence. A target that
// cannot carry the fence in the write itself (a barrier database's own
// store, which goes with the database's backend) checks the incarnation is
// still live first.
func (g logGroup) proposeCmd(ctx context.Context, cmd []byte) (sm.Result, error) {
	if g.fence.live == nil {
		return g.target.propose(ctx, cmd)
	}
	if fenced, ok := g.target.(fencedLogTarget); ok {
		return fenced.proposeFenced(ctx, cmd, g.fence)
	}
	if err := g.fence.live(); err != nil {
		return sm.Result{}, err
	}
	return g.target.propose(ctx, cmd)
}

// read answers one vlog.Request from the tenant's group. A log of a live
// database is read only while its descriptor records that database's
// incarnation (vlog.IncarnationRequest), so no log of an earlier database of
// the name is ever served to its successor.
func logRead[T any](ctx context.Context, route *logRoute, id vlog.LogID, req vlog.Request) (T, error) {
	var zero T
	target, db, err := route.resolve(ctx, id)
	if err != nil {
		return zero, err
	}
	if db != nil && id.TenantID != "" && id.Database != "" {
		req = vlog.IncarnationRequest{Request: req, Incarnation: db.Incarnation()}
	}
	return targetRead[T](ctx, target, req)
}

// groupRead answers req from the target g was admitted to, under the
// incarnation g was admitted under (when it has one), so a write built from
// what it reads is checked and proposed against one incarnation: a drop and
// re-create between the read and the propose cannot hand the predecessor's
// state to the successor.
func groupRead[T any](ctx context.Context, g logGroup, req vlog.Request) (T, error) {
	if g.incarnation != "" {
		req = vlog.IncarnationRequest{Request: req, Incarnation: g.incarnation}
	}
	return targetRead[T](ctx, g.target, req)
}

// targetRead answers one vlog.Request from target.
func targetRead[T any](ctx context.Context, target logTarget, req vlog.Request) (T, error) {
	var zero T
	result, err := target.read(ctx, req)
	if err != nil {
		return zero, err
	}
	v, ok := result.(T)
	if !ok {
		return zero, &unexpectedResultTypeError{}
	}
	return v, nil
}

// CreateLog creates a log and, when the request names a key type, its first
// head-signing key, which the HeadSignerProvider creates. The descriptor
// records the provider's reference and the public key; a key record the
// provider returns is written by the same proposal as the log.
func (s *logService) CreateLog(ctx context.Context, req *CreateLogRequest) (*CreateLogResponse, error) {
	id, err := s.logID(ctx, req.LogRef, "create a verifiable log")
	if err != nil {
		return nil, err
	}
	if vlog.IsAuditLog(id) {
		// The audit name belongs to the audit instance, which only an audit
		// event creates: no client log is created there, so none is minted
		// a key or proposed. The apply still accepts a create committed
		// before this refusal (docs/databases/architecture.md §7).
		return nil, &vlog.ReservedLogError{ID: id}
	}
	desc := vlog.Descriptor{
		Origin:            req.Origin,
		Hash:              vlog.HashSHA256,
		VerifierKeys:      req.VerifierKeys,
		AllowExpireBodies: req.AllowExpireBodies,
	}
	g, err := s.admit(ctx, id, logKeyFeatures)
	if err != nil {
		return nil, err
	}
	// The log records the incarnation of the database it is created in, so
	// a stale drop of an earlier incarnation leaves it alone (vlog.Chain.
	// DropDatabase) and a successor's reads refuse it. It is the one the
	// write was admitted under and is fenced to. A name no database holds
	// records none. Until featuregate.VerifiableLogDrop is active on the
	// group, a replica there would decode the descriptor without the field
	// and persist other bytes than its peers, so the log records none and is
	// a legacy log of the database holding its name.
	desc.Incarnation = g.incarnation
	if !s.route.fenced(id) {
		if db, err := s.route.databases.GetOwned(id.Database, id.TenantID); err == nil {
			desc.Incarnation = db.Incarnation()
		}
	}
	if !g.target.featureEnabled(featuregate.VerifiableLogDrop) {
		desc.Incarnation = ""
	}
	resp := &CreateLogResponse{ID: id}
	encode := func() ([]byte, error) { return protocol.EncodeVerifiableLogCreate(id, desc) }
	if req.SigningKeyType != "" {
		key, record, err := s.createKey(ctx, id, req.SigningKeyType, vlog.FirstKeyVersion)
		if err != nil {
			return nil, err
		}
		desc.Keys = []vlog.HeadKey{key}
		resp.Key = &key
		if record != nil {
			encode = func() ([]byte, error) { return protocol.EncodeVerifiableLogCreateWithKey(id, desc, *record) }
		}
	}
	if _, err := g.propose(ctx, id, encode); err != nil {
		return nil, err
	}
	return resp, nil
}

// RotateKey has the HeadSignerProvider create the log's next key and makes
// it the key that signs new heads. Earlier keys stay recorded, so every head
// already signed still verifies; of two rotations racing from the same key,
// the second to apply is refused as a conflict.
func (s *logService) RotateKey(ctx context.Context, req *RotateKeyRequest) (*RotateKeyResponse, error) {
	id, err := s.logID(ctx, req.LogRef, "rotate a verifiable log's key")
	if err != nil {
		return nil, err
	}
	desc, err := logRead[*vlog.Descriptor](ctx, s.route, id, vlog.DescriptorRequest{ID: id})
	if err != nil {
		return nil, err
	}
	features := logKeyFeatures
	if desc.Incarnation != "" {
		features = logIncarnationFeatures
	}
	g, err := s.admit(ctx, id, features)
	if err != nil {
		return nil, err
	}
	// The read above only chose the features to admit under; the key
	// version comes from the descriptor of the incarnation admitted.
	if desc, err = groupRead[*vlog.Descriptor](ctx, g, vlog.DescriptorRequest{ID: id}); err != nil {
		return nil, err
	}
	key, record, err := s.createKey(ctx, id, req.SigningKeyType, vlog.FirstKeyVersion+uint32(len(desc.Keys)))
	if err != nil {
		return nil, err
	}
	if _, err := g.propose(ctx, id, func() ([]byte, error) {
		return protocol.EncodeVerifiableLogRotateKey(id, key, record)
	}); err != nil {
		return nil, err
	}
	return &RotateKeyResponse{Key: key}, nil
}

// createKey asks the provider for id's key of type t at version and checks
// what it returns before anything is proposed.
func (s *logService) createKey(ctx context.Context, id vlog.LogID, t vlog.SignatureType, version uint32) (vlog.HeadKey, *vlog.SigningKey, error) {
	if err := t.Check(); err != nil {
		return vlog.HeadKey{}, nil, err
	}
	req := vlog.CreateKeyRequest{TenantID: id.TenantID, Database: id.Database, Log: id.Log, Type: t, Version: version}
	created, err := s.signers.CreateKey(ctx, req)
	if err != nil {
		return vlog.HeadKey{}, nil, err
	}
	key, err := created.HeadKey(req)
	if err != nil {
		return vlog.HeadKey{}, nil, err
	}
	return key, created.Record, nil
}

// ListLogs lists the logs in one of the caller's databases.
func (s *logService) ListLogs(ctx context.Context, req *ListLogsRequest) (*ListLogsResponse, error) {
	own, err := tenant.Confine(ctx, req.TenantID, "list verifiable logs")
	if err != nil {
		return nil, err
	}
	target := routingTenant(own)
	dbID := vlog.LogID{TenantID: target, Database: req.Database}
	listReq := vlog.ListRequest{TenantID: target, Database: req.Database}
	logs, err := logRead[[]vlog.LogID](ctx, s.route, dbID, listReq)
	if err != nil {
		return nil, err
	}
	if req.Database != vlog.AuditDatabase || s.route.audit == nil {
		return &ListLogsResponse{Logs: logs}, nil
	}
	// The audit log lives on the system shard while every other log of the
	// system database lives where the tenant's data does: merge the two.
	auditID := vlog.LogID{TenantID: target, Database: vlog.AuditDatabase, Log: vlog.AuditLog}
	audit, err := logRead[[]vlog.LogID](ctx, s.route, auditID, listReq)
	if err != nil {
		return nil, err
	}
	merged := make([]vlog.LogID, 0, len(logs)+1)
	for _, id := range logs {
		if !isAuditLog(id) {
			merged = append(merged, id)
		}
	}
	for _, id := range audit {
		if isAuditLog(id) {
			merged = append(merged, id)
		}
	}
	slices.SortFunc(merged, func(a, b vlog.LogID) int { return strings.Compare(a.Log, b.Log) })
	return &ListLogsResponse{Logs: merged}, nil
}

// isAuditLog reports whether id names a tenant's audit log (system/audit),
// the one log that is served by the system shard whatever the tenant's
// barrier: every other log of the system database is the tenant's own data.
func isAuditLog(id vlog.LogID) bool {
	return id.Database == vlog.AuditDatabase && id.Log == vlog.AuditLog
}

// Append appends entries to a log.
func (s *logService) Append(ctx context.Context, req *AppendRequest) (*AppendResponse, error) {
	id, err := s.logID(ctx, req.LogRef, "append to a verifiable log")
	if err != nil {
		return nil, err
	}
	if vlog.IsAuditLog(id) {
		// Only the replicated audit logger appends to the audit instance,
		// with its own sub-type: a client's entry is never an audit event.
		// A plain log created under the audit name before the instance
		// existed has no Audit in its descriptor and keeps its own entries;
		// the apply refuses the instance again from committed state.
		desc, err := logRead[*vlog.Descriptor](ctx, s.route, id, vlog.DescriptorRequest{ID: id})
		if err != nil {
			return nil, err
		}
		if desc.Audit {
			return nil, &vlog.ReservedLogError{ID: id}
		}
	}
	first, err := s.propose(ctx, id, logFeatures, func() ([]byte, error) {
		return protocol.EncodeVerifiableLogAppend(id, req.Entries)
	})
	if err != nil {
		return nil, err
	}
	return &AppendResponse{FirstIndex: first, Count: len(req.Entries)}, nil
}

// GetEntries reads a page of a log's entries.
func (s *logService) GetEntries(ctx context.Context, req *GetEntriesRequest) (*GetEntriesResponse, error) {
	id, err := s.logID(ctx, req.LogRef, "read a verifiable log")
	if err != nil {
		return nil, err
	}
	entries, err := logRead[[]vlog.Entry](ctx, s.route, id, vlog.GetRequest{ID: id, From: req.From, Limit: req.Limit})
	if err != nil {
		return nil, err
	}
	return &GetEntriesResponse{Entries: entries}, nil
}

// ProveInclusion returns the inclusion proof of one entry.
func (s *logService) ProveInclusion(ctx context.Context, req *InclusionRequest) (*InclusionResponse, error) {
	id, err := s.logID(ctx, req.LogRef, "prove inclusion in a verifiable log")
	if err != nil {
		return nil, err
	}
	hashes, err := logRead[[][]byte](ctx, s.route, id, vlog.InclusionRequest{ID: id, Index: req.Index, Size: req.Size})
	if err != nil {
		return nil, err
	}
	return &InclusionResponse{Index: req.Index, Size: req.Size, Hashes: hashes}, nil
}

// ProveConsistency returns the consistency proof between two tree sizes.
func (s *logService) ProveConsistency(ctx context.Context, req *ConsistencyRequest) (*ConsistencyResponse, error) {
	id, err := s.logID(ctx, req.LogRef, "prove consistency of a verifiable log")
	if err != nil {
		return nil, err
	}
	hashes, err := logRead[[][]byte](ctx, s.route, id, vlog.ConsistencyRequest{ID: id, Size1: req.Size1, Size2: req.Size2})
	if err != nil {
		return nil, err
	}
	return &ConsistencyResponse{Size1: req.Size1, Size2: req.Size2, Hashes: hashes}, nil
}

// Head reads a log's head.
func (s *logService) Head(ctx context.Context, req *HeadRequest) (*HeadResponse, error) {
	id, err := s.logID(ctx, req.LogRef, "read a verifiable log's head")
	if err != nil {
		return nil, err
	}
	head, err := logRead[*vlog.Head](ctx, s.route, id, vlog.HeadRequest{ID: id})
	if err != nil {
		return nil, err
	}
	return &HeadResponse{Head: *head}, nil
}

// SignHead signs the log's current head with the provider's signer for the
// log's current key, checks the signature against the public key the
// descriptor records, and records the checkpoint. Apply checks the signed
// root against the tree at that size, so a head signed while appends land is
// still recorded for the size it names, and refuses a second checkpoint for
// the same size.
func (s *logService) SignHead(ctx context.Context, req *SignHeadRequest) (*SignHeadResponse, error) {
	id, err := s.logID(ctx, req.LogRef, "sign a verifiable log's head")
	if err != nil {
		return nil, err
	}
	// Admit first: the descriptor and head are read, and the checkpoint
	// proposed, under the one incarnation admitted here, so a SignHead
	// paused in its signer across a drop and re-create is refused rather
	// than landing the predecessor's checkpoint in the successor.
	g, err := s.admit(ctx, id, logFeatures)
	if err != nil {
		return nil, err
	}
	desc, err := groupRead[*vlog.Descriptor](ctx, g, vlog.DescriptorRequest{ID: id})
	if err != nil {
		return nil, err
	}
	key, ok := desc.CurrentKey()
	if !ok {
		return nil, &vlog.KeyNotFoundError{ID: id, Version: vlog.FirstKeyVersion}
	}
	head, err := groupRead[*vlog.Head](ctx, g, vlog.HeadRequest{ID: id})
	if err != nil {
		return nil, err
	}
	signer, err := s.signers.HeadSigner(ctx, vlog.HeadSignerRequest{
		TenantID: id.TenantID, Database: id.Database, Log: id.Log, KeyRef: key.Ref, KeyVersion: key.Version,
	})
	if err != nil {
		return nil, err
	}
	signed, err := vlog.SignHead(ctx, signer, desc.Origin, head.Size, head.Root)
	if err != nil {
		return nil, err
	}
	if signed.KeyVersion != key.Version {
		return nil, &vlog.HeadSignatureError{Reason: "signer reports key version " + vlog.KeyRef(signed.KeyVersion) + ", want " + vlog.KeyRef(key.Version)}
	}
	note, err := vlog.RenderCheckpoint(desc.Origin, signed, signer.Name(), signer.KeyHash())
	if err != nil {
		return nil, err
	}
	if _, err := desc.VerifyCheckpoint(signer.Name(), note); err != nil {
		return nil, err
	}
	if _, err := g.propose(ctx, id, func() ([]byte, error) {
		return protocol.EncodeVerifiableLogCheckpoint(id, head.Size, head.Root, note)
	}); err != nil {
		return nil, err
	}
	return &SignHeadResponse{Head: signed, Checkpoint: note}, nil
}

// ExpireBodies removes the bodies of entries below the cutoff.
func (s *logService) ExpireBodies(ctx context.Context, req *ExpireBodiesRequest) (*ExpireBodiesResponse, error) {
	id, err := s.logID(ctx, req.LogRef, "expire a verifiable log's bodies")
	if err != nil {
		return nil, err
	}
	expired, err := s.propose(ctx, id, logFeatures, func() ([]byte, error) {
		return protocol.EncodeVerifiableLogExpireBodies(id, req.Cutoff)
	})
	if err != nil {
		return nil, err
	}
	return &ExpireBodiesResponse{Expired: expired}, nil
}

// DropDatabaseLogs reclaims every log of tenantID's database name, of the
// dropped incarnation, where its name routes once the database's own backend
// is gone: the tenant's Raft group (a barrier tenant's home group, else the
// system shard) or a barrier tenant's standalone barrier store. A barrier
// database's own store goes with its backend and holds nothing here. The
// command is bounded (vlog.Chain.DropDatabase), so it is proposed again until
// a step deletes nothing. database.Manager runs it inside the drop's fenced
// teardown, before the catalog record is deleted and the name released, so a
// failure leaves the drop retryable and a database created again under the
// name sees no old log or key. A database with no logs proposes nothing; one
// with logs is refused with *LogFeatureNotActiveError while
// featuregate.VerifiableLogDrop is not active on its group, because a replica
// without the sub-type would halt on it.
func (s *logService) DropDatabaseLogs(ctx context.Context, tenantID, name, incarnation string) error {
	tenantID = routingTenant(tenantID)
	g, ok, err := s.dropGroup(ctx, tenantID, name)
	if err != nil || !ok {
		return err
	}
	id := vlog.LogID{TenantID: tenantID, Database: name}
	for {
		deleted, err := g.propose(ctx, id, func() ([]byte, error) {
			return protocol.EncodeVerifiableLogDropDatabase(tenantID, name, incarnation)
		})
		if err != nil {
			return err
		}
		if deleted == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

// CheckDropDatabaseLogs answers, before a drop starts, the refusal
// DropDatabaseLogs would give inside its teardown: nil when the database has
// no logs to reclaim or its group can reclaim them, else the typed error. A
// drop checked first is refused with nothing fenced or changed.
func (s *logService) CheckDropDatabaseLogs(ctx context.Context, tenantID, name string) error {
	_, _, err := s.dropGroup(ctx, routingTenant(tenantID), name)
	return err
}

// dropGroup admits the reclamation of tenantID's database name's logs. ok is
// false when there is nothing to reclaim: a group where the verifiable log is
// not active, a volume- or raft-isolated tenant (whose audit trail cannot be
// placed, so no log of it was ever created), or a database with no logs.
func (s *logService) dropGroup(ctx context.Context, tenantID, name string) (logGroup, bool, error) {
	target, err := s.route.raft(ctx, tenantID)
	if err != nil {
		if _, dedicated := errors.AsType[*DedicatedTenantAuditNotSupportedError](err); dedicated {
			return logGroup{}, false, nil
		}
		return logGroup{}, false, err
	}
	if !target.featureEnabled(featuregate.VerifiableLog) {
		return logGroup{}, false, nil
	}
	if err := target.serving(); err != nil {
		return logGroup{}, false, err
	}
	logs, err := targetRead[[]vlog.LogID](ctx, target, vlog.ListRequest{TenantID: tenantID, Database: name})
	if err != nil {
		// A state machine with no verifiable-log case at all (the opt-in
		// "kv" engine) refuses every log request as an unsupported query
		// type: it holds no log, so its database has none to reclaim.
		if uqe, ok := errors.AsType[unsupportedQueryTypeErr](err); ok && uqe.UnsupportedQueryType() {
			return logGroup{}, false, nil
		}
		return logGroup{}, false, err
	}
	if len(logs) == 0 {
		return logGroup{}, false, nil
	}
	if !target.featureEnabled(featuregate.VerifiableLogDrop) {
		var shardID uint64
		if rt, ok := target.(raftTarget); ok {
			shardID = rt.shardID
		}
		return logGroup{}, false, &LogFeatureNotActiveError{TenantID: tenantID, ShardID: shardID, Feature: featuregate.VerifiableLogDrop}
	}
	return logGroup{target: target}, true, nil
}
