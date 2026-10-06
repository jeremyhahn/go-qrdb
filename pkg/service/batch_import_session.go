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
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	quicraft "github.com/jeremyhahn/go-quicraft/pkg"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Chunked import sessions
//
// See docs/transactions/architecture.md ("Import session") for the full
// design. A session's metadata and staged chunks are written directly into
// the state machine's reserved keyspace through protocol.OpImportStage
// (pkg/importstage) -- never an MVCC key, never through the KV engine's own
// Put path -- bound to the session's lease exactly as the lease table binds
// any other raw key, so revoking or expiring that lease deletes the record
// through the ordinary lease-expiry cascade. This is the SAME raw-keyspace
// treatment the lease table itself gets (pkg/lease), on every
// KVStateMachine-family sm_type and the MVCC engine alike, which is what
// makes a staged record structurally unreachable from a client's
// Get/Range/Put of the same literal key: go-qrdb#576's security review
// found the EARLIER design (hidden MVCC keys filtered only at enumeration,
// like "__sys:"/"__tbl:") was a discovery guard, not storage isolation --
// read, overwritten and forged through plain Get/Range/Put in a
// dedicated-raft database's own store. See pkg/importstage,
// pkg/engine/mvcc_import_stage.go and pkg/statemachine/kv_import_stage.go
// for where the records actually live.
// ---------------------------------------------------------------------------

// importSessionMeta is a chunked import session's metadata record, stored
// as JSON at importstage.MetaKey(sessionID). It carries everything
// ImportCommit needs to finish the import exactly as a single-message
// Import would have, everything ImportAppend needs to validate the next
// chunk, and the (tenant, identity) ImportAppend/ImportCommit/ImportAbort
// check the caller against.
type importSessionMeta struct {
	Tenant  string
	Subject string

	Format        string
	Filename      string
	KeyField      string
	ValueMode     string
	ValueField    string
	SkipHeader    bool
	Transactional bool
	KeyPrefix     string

	LeaseID int64

	// LastSeq is the sequence number of the last chunk ImportAppend
	// staged, or -1 when none has been staged yet. Int64, not uint64: -1
	// is the natural "nothing yet" sentinel, and a session never holds
	// anywhere near 2^63 chunks (ImportMaxChunksPerSession refuses it
	// long before then).
	LastSeq int64

	ChunkCount int
	TotalBytes int64
}

// callerIdentity returns the (tenant, subject) a chunked import session is
// bound to and checked against: the SAME tenant resolution every other
// data-plane operation uses (tenant.CallerTenant), and the authenticated
// identity's Subject, empty when ctx carries no identity (the embedded
// transport without auth middleware).
func callerIdentity(ctx context.Context) (tenantID, subject string) {
	tenantID = tenant.CallerTenant(ctx)
	if id := auth.IdentityFromContext(ctx); id != nil {
		subject = id.Subject
	}
	return tenantID, subject
}

// checkImportStagingEnabled refuses with *featuregate.NotActiveError until
// every replica of this shard's group can apply protocol.OpImportStage:
// go-quicraft replays a group's committed log to catch up a new member, so
// an opcode applied under a narrower membership that already used it would
// be in that log for a replica admitted later that cannot apply it.
func (s *BatchService) checkImportStagingEnabled(ctx context.Context) error {
	gated, ok := s.host.(FeatureGatedHost)
	if ok && !gated.FeatureEnabled(s.shardID, featuregate.ImportStaging) {
		return &featuregate.NotActiveError{Feature: featuregate.ImportStaging, ShardID: s.shardID}
	}
	_ = ctx
	return nil
}

// proposeImportStage encodes and proposes cmd, translating an import
// session quota refusal (encoded by the state machine as a generic
// AppErrorConflict -- see handleImportStage) into *ImportQuotaExceededError
// using the limit the caller already knows it asked for, since the
// refusal's own message carries no structured fields back across the
// Raft/service boundary.
func (s *BatchService) proposeImportStage(ctx context.Context, cmd protocol.ImportStageCommand) error {
	if err := s.checkImportStagingEnabled(ctx); err != nil {
		return err
	}
	encoded, err := protocol.EncodeImportStage(cmd)
	if err != nil {
		return &EncodeRequestError{Err: err}
	}
	result, err := propose(ctx, s.host, s.shardID, encoded)
	if err != nil {
		return translateProposeError(err)
	}
	if resultErr := checkResultError(result); resultErr != nil {
		if cmd.MaxSessions > 0 {
			var conflict *CommandConflictError
			if errors.As(resultErr, &conflict) {
				return &ImportQuotaExceededError{Kind: "sessions", Limit: int64(cmd.MaxSessions)}
			}
		}
		return resultErr
	}
	return nil
}

// getSessionMeta reads sessionID's metadata record. found is false when the
// session has none.
func (s *BatchService) getSessionMeta(ctx context.Context, sessionID string) (value []byte, found bool, err error) {
	result, err := s.syncReadRetrying(ctx, ImportStageMetaQuery{SessionID: sessionID})
	if err != nil {
		return nil, false, s.translateReadError(err)
	}
	v, _ := result.([]byte)
	return v, v != nil, nil
}

// getSessionChunk reads sessionID's chunk seq. found is false when the
// session has staged no chunk at seq.
func (s *BatchService) getSessionChunk(ctx context.Context, sessionID string, seq uint64) (value []byte, found bool, err error) {
	result, err := s.syncReadRetrying(ctx, ImportStageChunkQuery{SessionID: sessionID, Seq: seq})
	if err != nil {
		return nil, false, s.translateReadError(err)
	}
	v, _ := result.([]byte)
	return v, v != nil, nil
}

// importReadIndexRetryBudget bounds how long getSessionMeta and
// getSessionChunk retry a read that fails with quicraft.ErrReadIndexNotReady
// -- the expected, transient state of a leader that has not yet committed an
// entry in its term (see pkg/server/tenant_host.go's WaitForShardServing,
// which absorbs the same condition on a shard's first open). ImportCommit
// reads back every chunk ImportAppend staged through whatever replica led
// then; a leader change between the two hands that read to a leader that
// has not committed yet, and a real client retrying right after a failover
// hits exactly this, so the service absorbs it instead of surfacing a
// transient error to the caller.
const importReadIndexRetryBudget = 5 * time.Second

// importReadIndexRetryInterval is the poll spacing within
// importReadIndexRetryBudget.
const importReadIndexRetryInterval = 20 * time.Millisecond

// syncReadRetrying performs a linearizable read through s.host.SyncRead,
// retrying while the shard answers ErrReadIndexNotReady for up to
// importReadIndexRetryBudget. Any other error, ctx's own cancellation, or
// running out of budget returns immediately.
func (s *BatchService) syncReadRetrying(ctx context.Context, query interface{}) (interface{}, error) {
	deadline := time.Now().Add(importReadIndexRetryBudget)
	for {
		result, err := s.host.SyncRead(ctx, s.shardID, query)
		if err == nil || !errors.Is(err, quicraft.ErrReadIndexNotReady) || time.Now().After(deadline) {
			return result, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(importReadIndexRetryInterval):
		}
	}
}

// readSessionMeta reads and decodes sessionID's metadata record.
func (s *BatchService) readSessionMeta(ctx context.Context, sessionID string) (*importSessionMeta, error) {
	raw, found, err := s.getSessionMeta(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &ImportSessionNotFoundError{SessionID: sessionID}
	}
	var meta importSessionMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, &ImportSessionCorruptError{SessionID: sessionID, Reason: "metadata does not decode: " + err.Error()}
	}
	return &meta, nil
}

// checkSessionOwner refuses with *ImportSessionOwnerError unless ctx's
// caller is the SAME (tenant, identity) ImportBegin recorded for meta,
// checked explicitly on both dimensions rather than inferred from the
// session ID being hard to guess.
func checkSessionOwner(ctx context.Context, sessionID string, meta *importSessionMeta) error {
	callerTenant, callerSubject := callerIdentity(ctx)
	if callerTenant != meta.Tenant || callerSubject != meta.Subject {
		return &ImportSessionOwnerError{SessionID: sessionID}
	}
	return nil
}

// ImportBegin starts a chunked import session scoped to ctx's caller. See
// BatchServicer.ImportBegin.
//
// The returned session ID carries opts.KeyPrefix as its own prefix, so
// that it alone (with no other context) tells ImportAppend/ImportCommit/
// ImportAbort which (tenant, database) namespace's reserved keyspace to
// read and write -- the same composite namespace a namespaced caller's
// KeyPrefix already names. A caller with no namespace (the unscoped,
// system-tenant path) gets a session ID with no prefix of its own,
// exactly as it always has for every other operation.
//
// The open-session quota is checked and the metadata record written in
// ONE proposal (see proposeImportStage/handleImportStage): the count and
// the write commit at the SAME log position, so two concurrent ImportBegin
// calls can never both observe room for the last slot.
func (s *BatchService) ImportBegin(ctx context.Context, opts ImportOptions) (string, error) {
	if err := validateImportOptions(opts); err != nil {
		return "", err
	}

	granted, err := s.leaseService().Grant(ctx, s.importSessionTTLSeconds())
	if err != nil {
		return "", err
	}

	callerTenant, callerSubject := callerIdentity(ctx)
	meta := importSessionMeta{
		Tenant:        callerTenant,
		Subject:       callerSubject,
		Format:        opts.Format,
		Filename:      opts.Filename,
		KeyField:      opts.KeyField,
		ValueMode:     opts.ValueMode,
		ValueField:    opts.ValueField,
		SkipHeader:    opts.SkipHeader,
		Transactional: opts.Transactional,
		KeyPrefix:     opts.KeyPrefix,
		LeaseID:       granted.ID,
		LastSeq:       -1,
	}
	data, err := json.Marshal(meta)
	if err != nil {
		_ = s.leaseService().Revoke(ctx, granted.ID)
		return "", &EncodeRequestError{Err: err}
	}

	sessionID := opts.KeyPrefix + rand.Text()
	err = s.proposeImportStage(ctx, protocol.ImportStageCommand{
		Kind:        protocol.ImportStageSetMeta,
		SessionID:   sessionID,
		LeaseID:     granted.ID,
		Body:        data,
		MaxSessions: uint32(s.importMaxSessionsPerTenant()), //nolint:gosec // G115: the configured limit is a small positive int
		TenantID:    callerTenant,
	})
	if err != nil {
		// An outcome-unknown proposal (ErrOutcomeUnknown) may have actually
		// bound this lease to a committed session record -- go-quicraft
		// committed the SetMeta entry and only failed to deliver the
		// response. Revoking here would destroy a session this call cannot
		// prove never existed, so leave it for its own TTL to reclaim
		// instead, exactly like ImportCommit's best-effort release does.
		// Only a proven refusal (the lease was granted but never bound to
		// anything) is safe to revoke outright.
		if !errors.Is(err, ErrOutcomeUnknown) {
			_ = s.leaseService().Revoke(ctx, granted.ID)
		}
		return "", err
	}

	return encodeSessionID(sessionID), nil
}

// encodeSessionID and decodeSessionID translate between the internal
// session ID ImportBegin constructs (opts.KeyPrefix + a random suffix,
// used directly as part of the reserved storage key) and the external
// session ID every caller actually sees.
//
// The internal form can contain "/" -- KeyPrefix is a "<tenant>/<db>/"
// style composite namespace -- which every transport that carries a
// session ID as a single opaque token (a REST path segment matched one
// segment at a time, a QUIC/MCP string field a CLI round-trips verbatim)
// would otherwise either mishandle or have to escape its own way, N
// times, once per transport. Encoding it once, here, at the one place the
// ID is minted and read back, means no transport needs to know the
// internal form has structure at all.
func encodeSessionID(internalID string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(internalID))
}

// decodeSessionID reverses encodeSessionID. A session ID that does not
// decode is reported exactly like an unknown one
// (*ImportSessionNotFoundError): a malformed token and a stale one look
// identical from the caller's side, and neither should report anything
// about the internal form's structure.
func decodeSessionID(externalID string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(externalID)
	if err != nil {
		return "", &ImportSessionNotFoundError{SessionID: externalID}
	}
	return string(raw), nil
}

// ImportSessionHasPrefix reports whether externalID's internal form --
// recovered the same way ImportAppend/ImportCommit/ImportAbort recover it,
// through decodeSessionID -- starts with prefix: the composite (tenant,
// database) namespace ImportBegin folded into the session ID it minted
// (see ImportBegin's doc comment). A malformed externalID answers false, the
// same as an externalID whose internal form simply does not start with
// prefix, so a caller that only means to refuse a cross-database session
// with *ImportSessionNotFoundError (decodeSessionID's own answer for a
// malformed token) does not need its own case for "did not even decode".
//
// This exists for pkg/server's per-database namespacedBatchService: without
// it, ImportAppend/ImportCommit/ImportAbort check only the caller's (tenant,
// identity) against what ImportBegin recorded (checkSessionOwner), never
// the database -- so the SAME tenant, calling through a DIFFERENT
// namespaced database wrapper than the one ImportBegin was called through,
// could append to, commit, or abort a session it never began there
// (go-qrdb#576's security review).
func ImportSessionHasPrefix(externalID, prefix string) bool {
	internalID, err := decodeSessionID(externalID)
	if err != nil {
		return false
	}
	return strings.HasPrefix(internalID, prefix)
}

// ImportAppend stages chunk as sequence seq of sessionID. See
// BatchServicer.ImportAppend.
func (s *BatchService) ImportAppend(ctx context.Context, sessionID string, seq uint64, chunk []byte) error {
	internalID, err := decodeSessionID(sessionID)
	if err != nil {
		return err
	}
	sessionID = internalID

	meta, err := s.readSessionMeta(ctx, sessionID)
	if err != nil {
		return err
	}
	if err := checkSessionOwner(ctx, sessionID, meta); err != nil {
		return err
	}

	got := int64(seq)
	want := meta.LastSeq + 1

	switch {
	case got == want:
		// A new chunk: proceed below.

	case meta.LastSeq >= 0 && got == meta.LastSeq:
		// A possible replay: idempotent only if the bytes match exactly
		// what is already staged at this sequence.
		existing, found, err := s.getSessionChunk(ctx, sessionID, seq)
		if err != nil {
			return err
		}
		if !found || !bytes.Equal(existing, chunk) {
			return &ImportSequenceError{SessionID: sessionID, Got: seq, Want: uint64(want)}
		}
		// Identical replay: renew the lease and report success without
		// restaging anything or re-admitting the quota.
		return s.renewSessionLease(ctx, sessionID, meta.LeaseID)

	default:
		return &ImportSequenceError{SessionID: sessionID, Got: seq, Want: uint64(want)}
	}

	newTotal := meta.TotalBytes + int64(len(chunk))
	if newTotal > s.importMaxSessionBytes() {
		return &ImportQuotaExceededError{Kind: "bytes", Limit: s.importMaxSessionBytes()}
	}
	newCount := meta.ChunkCount + 1
	if newCount > s.importMaxChunksPerSession() {
		return &ImportQuotaExceededError{Kind: "chunks", Limit: int64(s.importMaxChunksPerSession())}
	}

	meta.LastSeq = got
	meta.ChunkCount = newCount
	meta.TotalBytes = newTotal
	data, err := json.Marshal(meta)
	if err != nil {
		return &EncodeRequestError{Err: err}
	}

	// The chunk and the metadata that now accounts for it commit in ONE
	// proposal (MetaBody), exactly as the two Raft commands this session
	// used to carry as putRawBatch's single atomic batch.
	if err := s.proposeImportStage(ctx, protocol.ImportStageCommand{
		Kind:      protocol.ImportStageSetChunk,
		SessionID: sessionID,
		LeaseID:   meta.LeaseID,
		Seq:       seq,
		Body:      chunk,
		MetaBody:  data,
	}); err != nil {
		return err
	}

	return s.renewSessionLease(ctx, sessionID, meta.LeaseID)
}

// renewSessionLease renews leaseID so an active session never expires
// mid-upload. A lease the Lessor no longer holds is reported as
// *ImportSessionNotFoundError: the session already expired out from under
// this call (a race between an abandoned-session reclaim and a very late
// retry), which is the same condition a fresh readSessionMeta would now
// report.
func (s *BatchService) renewSessionLease(ctx context.Context, sessionID string, leaseID int64) error {
	if _, err := s.leaseService().KeepAlive(ctx, leaseID); err != nil {
		var gone *LeaseNotFoundError
		if errors.As(err, &gone) {
			return &ImportSessionNotFoundError{SessionID: sessionID}
		}
		return err
	}
	return nil
}

// ImportCommit applies every staged chunk of sessionID atomically. See
// BatchServicer.ImportCommit.
func (s *BatchService) ImportCommit(ctx context.Context, sessionID string) (*ImportResult, error) {
	internalID, err := decodeSessionID(sessionID)
	if err != nil {
		return nil, err
	}
	sessionID = internalID

	meta, err := s.readSessionMeta(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if err := checkSessionOwner(ctx, sessionID, meta); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	for seq := int64(0); seq <= meta.LastSeq; seq++ {
		chunk, found, err := s.getSessionChunk(ctx, sessionID, uint64(seq))
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, &ImportSessionCorruptError{
				SessionID: sessionID,
				Reason:    "metadata records a chunk storage does not hold",
			}
		}
		buf.Write(chunk)
	}

	opts := ImportOptions{
		Format:        meta.Format,
		Filename:      meta.Filename,
		KeyField:      meta.KeyField,
		ValueMode:     meta.ValueMode,
		ValueField:    meta.ValueField,
		SkipHeader:    meta.SkipHeader,
		Transactional: meta.Transactional,
		KeyPrefix:     meta.KeyPrefix,
	}

	// A failure here applies nothing (every write path below it is a
	// single atomic proposal or refuses outright) and leaves the session
	// -- its metadata and every staged chunk -- untouched, so the caller
	// can inspect the error and retry ImportCommit without re-uploading.
	result, err := s.parseAndImport(ctx, buf.Bytes(), opts)
	if err != nil {
		return nil, err
	}

	// Best-effort release: the import already committed, so a failure to
	// revoke the lease here only delays cleanup until its TTL elapses on
	// its own, exactly as an abandoned session would.
	_ = s.leaseService().Revoke(ctx, meta.LeaseID)

	return result, nil
}

// ImportAbort releases sessionID without applying anything. See
// BatchServicer.ImportAbort.
func (s *BatchService) ImportAbort(ctx context.Context, sessionID string) error {
	internalID, err := decodeSessionID(sessionID)
	if err != nil {
		return err
	}
	sessionID = internalID

	meta, err := s.readSessionMeta(ctx, sessionID)
	if err != nil {
		return err
	}
	if err := checkSessionOwner(ctx, sessionID, meta); err != nil {
		return err
	}

	if err := s.leaseService().Revoke(ctx, meta.LeaseID); err != nil {
		// A lease already gone has already deleted everything bound to
		// it -- the metadata record and every staged chunk -- which is
		// exactly what ImportAbort is for, so this is success, not an
		// error to surface.
		var gone *LeaseNotFoundError
		if !errors.As(err, &gone) {
			return err
		}
	}

	return nil
}
