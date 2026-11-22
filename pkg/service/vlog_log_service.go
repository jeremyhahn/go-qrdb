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
	"sync"

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
	route := &logRoute{resolver: cfg.Resolver, databases: cfg.Databases, stores: make(map[dbStoreKey]*databaseLogStore), barriers: make(map[string]*barrierLogStore)}
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
	return logRead[*vlog.SigningKey](ctx, r.route, id.TenantID, id.Database, vlog.KeyRequest{ID: id, Version: version})
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
// target its logs are applied in and read from.
func (r *logRoute) resolve(ctx context.Context, tenantID, name string) (logTarget, error) {
	db, err := r.databases.GetOwned(name, tenantID)
	if err != nil {
		var notFound *database.NotFoundError
		if !errors.As(err, &notFound) {
			return nil, err
		}
		return r.raft(ctx, tenantID)
	}
	if db.BarrierEnabled() {
		cfg := db.Config()
		if !cfg.BarrierOnDataPath() {
			return nil, &database.BarrierOffDataPathError{Name: name, ServedBy: database.ServedByRaftGroup}
		}
		return r.store(tenantID, name, db), nil
	}
	return r.raft(ctx, tenantID)
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
	s, err := newBarrierLogStore(tenantID, barrier, kv)
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
	databases LogDatabaseLookup

	mu       sync.Mutex
	stores   map[dbStoreKey]*databaseLogStore
	barriers map[string]*barrierLogStore
}

// dbStoreKey names one incarnation of a tenant's database.
type dbStoreKey struct{ tenant, name, incarnation string }

// logTarget is where a log's commands are applied and its reads answered.
type logTarget interface {
	propose(ctx context.Context, cmd []byte) (sm.Result, error)
	read(ctx context.Context, req vlog.Request) (interface{}, error)
	featureEnabled(f feature.Feature) bool
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
)

// propose sends one encoded OpVerifiableLog command to the tenant's group,
// refused while any of features is not active there, and returns the apply's
// value.
func (s *logService) propose(ctx context.Context, id vlog.LogID, features []feature.Feature, encode func() ([]byte, error)) (uint64, error) {
	g, err := s.admit(ctx, id.TenantID, id.Database, features)
	if err != nil {
		return 0, err
	}
	return g.propose(ctx, id, encode)
}

// logGroup is the target a log write has been admitted to.
type logGroup struct {
	target logTarget
}

// admit resolves the tenant's target and refuses the write while any of
// features is not active there. A write that mints a key admits first, so a
// refused tenant never has a key created for it.
func (s *logService) admit(ctx context.Context, tenantID, db string, features []feature.Feature) (logGroup, error) {
	target, err := s.route.resolve(ctx, tenantID, db)
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
	return logGroup{target: target}, nil
}

// propose sends one encoded OpVerifiableLog command for the log id to g and
// returns the apply's value.
func (g logGroup) propose(ctx context.Context, id vlog.LogID, encode func() ([]byte, error)) (uint64, error) {
	cmd, err := encode()
	if err != nil {
		return 0, &EncodeRequestError{Err: err}
	}
	result, err := g.target.propose(ctx, cmd)
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

// read answers one vlog.Request from the tenant's group.
func logRead[T any](ctx context.Context, route *logRoute, tenantID, db string, req vlog.Request) (T, error) {
	var zero T
	target, err := route.resolve(ctx, tenantID, db)
	if err != nil {
		return zero, err
	}
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
	g, err := s.admit(ctx, id.TenantID, id.Database, logKeyFeatures)
	if err != nil {
		return nil, err
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
	desc, err := logRead[*vlog.Descriptor](ctx, s.route, id.TenantID, id.Database, vlog.DescriptorRequest{ID: id})
	if err != nil {
		return nil, err
	}
	g, err := s.admit(ctx, id.TenantID, id.Database, logKeyFeatures)
	if err != nil {
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
	logs, err := logRead[[]vlog.LogID](ctx, s.route, target, req.Database, vlog.ListRequest{TenantID: target, Database: req.Database})
	if err != nil {
		return nil, err
	}
	return &ListLogsResponse{Logs: logs}, nil
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
		desc, err := logRead[*vlog.Descriptor](ctx, s.route, id.TenantID, id.Database, vlog.DescriptorRequest{ID: id})
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
	entries, err := logRead[[]vlog.Entry](ctx, s.route, id.TenantID, id.Database, vlog.GetRequest{ID: id, From: req.From, Limit: req.Limit})
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
	hashes, err := logRead[[][]byte](ctx, s.route, id.TenantID, id.Database, vlog.InclusionRequest{ID: id, Index: req.Index, Size: req.Size})
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
	hashes, err := logRead[[][]byte](ctx, s.route, id.TenantID, id.Database, vlog.ConsistencyRequest{ID: id, Size1: req.Size1, Size2: req.Size2})
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
	head, err := logRead[*vlog.Head](ctx, s.route, id.TenantID, id.Database, vlog.HeadRequest{ID: id})
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
	desc, err := logRead[*vlog.Descriptor](ctx, s.route, id.TenantID, id.Database, vlog.DescriptorRequest{ID: id})
	if err != nil {
		return nil, err
	}
	key, ok := desc.CurrentKey()
	if !ok {
		return nil, &vlog.KeyNotFoundError{ID: id, Version: vlog.FirstKeyVersion}
	}
	head, err := logRead[*vlog.Head](ctx, s.route, id.TenantID, id.Database, vlog.HeadRequest{ID: id})
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
	if _, err := s.propose(ctx, id, logFeatures, func() ([]byte, error) {
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
