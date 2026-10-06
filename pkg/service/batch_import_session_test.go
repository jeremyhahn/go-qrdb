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
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/importstage"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// importSessionKeyNotFoundError satisfies the keyNotFoundErr interface
// (mvcc_service.go) getRaw checks for, so a missing key reads back as
// "not found", not an error.
type importSessionKeyNotFoundError struct{ key string }

func (e *importSessionKeyNotFoundError) Error() string       { return "not found: " + e.key }
func (e *importSessionKeyNotFoundError) KeyNotFound() string { return e.key }

// importSessionStore backs newImportSessionHost: an in-memory map and
// lease-revocation set that really decodes and applies exactly the
// commands BatchService's session methods (and the LeaseService they
// compose) issue -- protocol.OpImportStage for a session's own metadata
// and chunks (raw, never through "kv", exactly like the real engines'
// handleImportStage), and protocol.OpBatch/OpMVCCPut for the actual rows
// ImportCommit's parseAndImport proposes -- so a test can read back what a
// PREVIOUS call in the same test actually staged or committed, not a
// canned, scripted response.
//
// It intentionally supports nothing beyond that surface: a session test
// that needs more belongs against a real harness (raftharness), not a
// deeper fake here.
type importSessionStore struct {
	mu      sync.Mutex
	kv      map[string][]byte // ordinary rows ImportCommit's parseAndImport writes
	staged  map[string][]byte // pkg/importstage raw records (meta + chunks)
	leaseOf map[string]int64  // staged key -> bound lease ID

	revoked     map[int64]bool
	nextLeaseID int64
}

// newImportSessionHost returns a mockHost (service_test.go) wired to a
// fresh importSessionStore, so every other Host method a test does not
// care about keeps mockHost's harmless zero-value defaults.
func newImportSessionHost() *mockHost {
	host, _ := newImportSessionHostWithStore()
	return host
}

// newImportSessionHostWithStore is newImportSessionHost plus the backing
// store itself, for a test that needs to inspect what was actually
// committed (e.g. "was this row's key ever written") rather than only the
// ImportResult BatchService returns.
func newImportSessionHostWithStore() (*mockHost, *importSessionStore) {
	st := &importSessionStore{
		kv:      make(map[string][]byte),
		staged:  make(map[string][]byte),
		leaseOf: make(map[string]int64),
		revoked: make(map[int64]bool),
	}
	return &mockHost{
		syncProposeFn: st.syncPropose,
		syncReadFn:    st.syncRead,
	}, st
}

func (st *importSessionStore) syncPropose(_ context.Context, _ uint64, cmd []byte) (sm.Result, error) {
	st.mu.Lock()
	defer st.mu.Unlock()

	decoded, err := protocol.DecodeCommand(cmd)
	if err != nil {
		return sm.Result{}, err
	}

	switch decoded.Op {
	case protocol.OpBatch:
		ops, err := protocol.DecodeBatch(decoded.Payload)
		if err != nil {
			return sm.Result{}, err
		}
		for _, op := range ops {
			switch op.Type {
			case protocol.OpMVCCPut:
				st.kv[string(op.Key)] = op.Value
			case protocol.OpMVCCDelete:
				delete(st.kv, string(op.Key))
			}
		}
		return sm.Result{Value: 1}, nil

	case protocol.OpImportStage:
		return st.applyImportStage(decoded.Payload)

	case protocol.OpLeaseGrant:
		st.nextLeaseID++
		return sm.Result{Value: uint64(st.nextLeaseID)}, nil

	case protocol.OpLeaseRevoke:
		id, err := protocol.DecodeLeaseRevoke(decoded.Payload)
		if err != nil {
			return sm.Result{}, err
		}
		if st.revoked[id] {
			return sm.Result{}, &LeaseNotFoundError{LeaseID: id}
		}
		st.revoked[id] = true
		for k, lid := range st.leaseOf {
			if lid == id {
				delete(st.staged, k)
				delete(st.leaseOf, k)
			}
		}
		return sm.Result{Value: 1}, nil

	case protocol.OpLeaseKeepAlive:
		id, err := protocol.DecodeLeaseKeepAlive(decoded.Payload)
		if err != nil {
			return sm.Result{}, err
		}
		if st.revoked[id] {
			return sm.Result{}, &LeaseNotFoundError{LeaseID: id}
		}
		return sm.Result{Value: 1}, nil

	default:
		return sm.Result{}, &protocol.InvalidCommandError{Reason: "importSessionStore: unsupported op code"}
	}
}

// applyImportStage is the fake's handleImportStage: it writes raw, staged
// records bound to a lease, with the SAME atomic open-session quota check
// the real engines run at the same log position as the write, so
// TestImportBegin_SessionsPerTenantQuota exercises the actual race-closing
// behavior, not a canned refusal. The caller holds st.mu.
func (st *importSessionStore) applyImportStage(payload []byte) (sm.Result, error) {
	cmd, err := protocol.DecodeImportStage(payload)
	if err != nil {
		return sm.Result{}, err
	}
	if cmd.LeaseID == 0 || st.revoked[cmd.LeaseID] {
		return sm.Result{Data: protocol.EncodeLeaseNotFoundError(cmd.LeaseID)}, nil
	}

	type write struct {
		key   []byte
		value []byte
	}
	var writes []write
	switch cmd.Kind {
	case protocol.ImportStageSetMeta:
		if cmd.MaxSessions > 0 {
			prefix := string(importstage.TenantIndexScanPrefix(cmd.TenantID))
			open := 0
			for k := range st.staged {
				if strings.HasPrefix(k, prefix) {
					open++
				}
			}
			if open >= int(cmd.MaxSessions) {
				return sm.Result{Data: protocol.EncodeAppError(protocol.AppErrorConflict, "import session quota exceeded")}, nil
			}
		}
		writes = []write{
			{importstage.MetaKey(cmd.SessionID), cmd.Body},
			{importstage.TenantIndexKey(cmd.TenantID, cmd.SessionID), nil},
		}
	case protocol.ImportStageSetChunk:
		writes = []write{{importstage.ChunkKey(cmd.SessionID, cmd.Seq), cmd.Body}}
		if len(cmd.MetaBody) > 0 {
			writes = append(writes, write{importstage.MetaKey(cmd.SessionID), cmd.MetaBody})
		}
	default:
		return sm.Result{}, &protocol.InvalidCommandError{Reason: "import stage: unknown kind"}
	}
	for _, w := range writes {
		st.staged[string(w.key)] = w.value
		st.leaseOf[string(w.key)] = cmd.LeaseID
	}
	return sm.Result{Value: 1}, nil
}

// has reports whether key was ever actually written to the store, for a
// test that needs to prove a row's write was (or was not) proposed and
// applied, not merely inferred from the ImportResult counts.
func (st *importSessionStore) has(key string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	_, ok := st.kv[key]
	return ok
}

func (st *importSessionStore) syncRead(_ context.Context, _ uint64, query interface{}) (interface{}, error) {
	st.mu.Lock()
	defer st.mu.Unlock()

	switch q := query.(type) {
	case MVCCGetQuery:
		v, ok := st.kv[string(q.Key)]
		if !ok {
			return nil, &importSessionKeyNotFoundError{key: string(q.Key)}
		}
		return &KeyValue{Key: q.Key, Value: v}, nil

	case MVCCRangeQuery:
		var kvs []*KeyValue
		for k, v := range st.kv {
			if k >= string(q.Start) && (len(q.End) == 0 || k < string(q.End)) {
				kvs = append(kvs, &KeyValue{Key: []byte(k), Value: v})
			}
		}
		return &MVCCRangeResult{KeyValues: kvs, Count: int64(len(kvs))}, nil

	case ImportStageMetaQuery:
		v, ok := st.staged[string(importstage.MetaKey(q.SessionID))]
		if !ok {
			return nil, nil
		}
		return v, nil

	case ImportStageChunkQuery:
		v, ok := st.staged[string(importstage.ChunkKey(q.SessionID, q.Seq))]
		if !ok {
			return nil, nil
		}
		return v, nil

	case ImportStageOpenSessionsQuery:
		prefix := string(importstage.TenantIndexScanPrefix(q.TenantID))
		count := 0
		for k := range st.staged {
			if strings.HasPrefix(k, prefix) {
				count++
			}
		}
		return count, nil

	case LeaseTimeToLiveQuery:
		if st.revoked[q.ID] {
			return nil, &LeaseNotFoundError{LeaseID: q.ID}
		}
		return &Lease{ID: q.ID, GrantedTTL: 3600, RemainingTTL: 3600}, nil

	default:
		return nil, &unexpectedResultTypeError{}
	}
}

// withCaller returns ctx carrying tenantID and subject as the authenticated
// caller, the same way the auth middleware and tenant resolution bind them
// on every real transport.
func withCaller(ctx context.Context, tenantID, subject string) context.Context {
	ctx = tenant.WithDatabase(ctx, "")
	ctx = auth.WithIdentity(ctx, &auth.Identity{TenantID: tenantID, Subject: subject})
	return ctx
}

func TestImportKeyEncoding(t *testing.T) {
	mk := importstage.MetaKey("sess1")
	if !strings.HasPrefix(string(mk), importstage.TablePrefix) {
		t.Errorf("meta key %q does not start with the reserved prefix", mk)
	}
	if !strings.HasSuffix(string(mk), "/meta") {
		t.Errorf("meta key %q does not end with /meta", mk)
	}

	c0 := importstage.ChunkKey("sess1", 0)
	c1 := importstage.ChunkKey("sess1", 1)
	if string(c0) >= string(c1) {
		t.Errorf("chunk keys are not in sequence order: %q >= %q", c0, c1)
	}
	if !strings.HasPrefix(string(c0), importstage.TablePrefix) {
		t.Errorf("chunk key %q does not start with the reserved prefix", c0)
	}
}

// TestImportBegin_SessionIDIsOneOpaqueTokenWithNoSlash proves the session
// ID ImportBegin hands back is safe to carry as a single REST path
// segment, a single gRPC/QUIC/MCP string field, or a single CLI argument on
// every transport, even when the caller's KeyPrefix (a "<tenant>/<db>/"
// composite namespace, which legitimately contains "/") is folded into it
// internally. A session ID containing "/" broke exactly this on REST,
// where chi matches a path segment up to (not across) a "/" -- the
// regression this test exists to catch again.
func TestImportBegin_SessionIDIsOneOpaqueTokenWithNoSlash(t *testing.T) {
	host := newImportSessionHost()
	svc := NewBatchService(host, 1)
	ctx := withCaller(context.Background(), "acme", "alice")

	sessionID, err := svc.ImportBegin(ctx, ImportOptions{
		Format: "csv", KeyField: "id", ValueMode: "row",
		// The exact shape namespacedBatchService.ImportBegin sets:
		// opts.KeyPrefix = s.prefix + opts.KeyPrefix.
		KeyPrefix: "acme/billing/",
	})
	if err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}
	if strings.Contains(sessionID, "/") {
		t.Errorf("session ID %q contains a slash", sessionID)
	}

	// And the round trip through Append/Commit still works with a
	// namespaced session ID.
	if err := svc.ImportAppend(ctx, sessionID, 0, []byte("id,name\nk1,Alice\n")); err != nil {
		t.Fatalf("ImportAppend: %v", err)
	}
	if _, err := svc.ImportCommit(ctx, sessionID); err != nil {
		t.Fatalf("ImportCommit: %v", err)
	}
}

// TestImportAppend_MalformedSessionIDRefused proves a session ID that
// does not decode is refused as not-found, the same answer an unknown (but
// well-formed) session ID gets, rather than leaking anything about the
// internal encoding.
func TestImportAppend_MalformedSessionIDRefused(t *testing.T) {
	host := newImportSessionHost()
	svc := NewBatchService(host, 1)
	ctx := withCaller(context.Background(), "acme", "alice")

	err := svc.ImportAppend(ctx, "not valid base64url!!", 0, []byte("x"))
	if !errors.As(err, new(*ImportSessionNotFoundError)) {
		t.Fatalf("error = %T (%v), want *ImportSessionNotFoundError", err, err)
	}
}

func TestImportBeginAppendCommit_HappyPath(t *testing.T) {
	host := newImportSessionHost()
	svc := NewBatchService(host, 1)
	ctx := withCaller(context.Background(), "acme", "alice")

	sessionID, err := svc.ImportBegin(ctx, ImportOptions{
		Format: "csv", KeyField: "id", ValueMode: "row", SkipHeader: true,
	})
	if err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}
	if sessionID == "" {
		t.Fatal("ImportBegin returned an empty session ID")
	}

	if err := svc.ImportAppend(ctx, sessionID, 0, []byte("id,name\n")); err != nil {
		t.Fatalf("ImportAppend(seq=0): %v", err)
	}
	if err := svc.ImportAppend(ctx, sessionID, 1, []byte("k1,Alice\n")); err != nil {
		t.Fatalf("ImportAppend(seq=1): %v", err)
	}

	result, err := svc.ImportCommit(ctx, sessionID)
	if err != nil {
		t.Fatalf("ImportCommit: %v", err)
	}
	if result.Total != 1 || result.Succeeded != 1 {
		t.Fatalf("ImportCommit result = %+v, want Total=1 Succeeded=1", result)
	}

	// The session is released on a successful commit: a second commit
	// must report the session gone, not re-apply anything.
	if _, err := svc.ImportCommit(ctx, sessionID); err == nil {
		t.Fatal("expected a second ImportCommit on an already-committed session to fail")
	} else if !errors.As(err, new(*ImportSessionNotFoundError)) {
		t.Errorf("second ImportCommit error = %T, want *ImportSessionNotFoundError", err)
	}
}

func TestImportAppend_IdempotentReplay(t *testing.T) {
	host := newImportSessionHost()
	svc := NewBatchService(host, 1)
	ctx := withCaller(context.Background(), "acme", "alice")

	sessionID, err := svc.ImportBegin(ctx, ImportOptions{Format: "csv", KeyField: "id", ValueMode: "row"})
	if err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}

	chunk := []byte("id,name\nk1,Alice\n")
	if err := svc.ImportAppend(ctx, sessionID, 0, chunk); err != nil {
		t.Fatalf("first ImportAppend: %v", err)
	}
	// An identical replay of the same sequence is a no-op success.
	if err := svc.ImportAppend(ctx, sessionID, 0, chunk); err != nil {
		t.Fatalf("replay ImportAppend: %v", err)
	}
}

func TestImportAppend_SequenceGapRefused(t *testing.T) {
	host := newImportSessionHost()
	svc := NewBatchService(host, 1)
	ctx := withCaller(context.Background(), "acme", "alice")

	sessionID, err := svc.ImportBegin(ctx, ImportOptions{Format: "csv", KeyField: "id", ValueMode: "row"})
	if err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}

	// seq 1 with nothing appended yet (want 0) is a gap.
	err = svc.ImportAppend(ctx, sessionID, 1, []byte("x"))
	if !errors.As(err, new(*ImportSequenceError)) {
		t.Fatalf("error = %T (%v), want *ImportSequenceError", err, err)
	}
}

func TestImportAppend_SequenceRegressionWithDifferentBytesRefused(t *testing.T) {
	host := newImportSessionHost()
	svc := NewBatchService(host, 1)
	ctx := withCaller(context.Background(), "acme", "alice")

	sessionID, err := svc.ImportBegin(ctx, ImportOptions{Format: "csv", KeyField: "id", ValueMode: "row"})
	if err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}
	if err := svc.ImportAppend(ctx, sessionID, 0, []byte("original")); err != nil {
		t.Fatalf("ImportAppend(seq=0): %v", err)
	}
	err = svc.ImportAppend(ctx, sessionID, 0, []byte("different-bytes"))
	if !errors.As(err, new(*ImportSequenceError)) {
		t.Fatalf("error = %T (%v), want *ImportSequenceError", err, err)
	}
}

func TestImportAppend_WrongCallerRefused(t *testing.T) {
	host := newImportSessionHost()
	svc := NewBatchService(host, 1)
	begun := withCaller(context.Background(), "acme", "alice")

	sessionID, err := svc.ImportBegin(begun, ImportOptions{Format: "csv", KeyField: "id", ValueMode: "row"})
	if err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}

	// Same tenant, different identity.
	other := withCaller(context.Background(), "acme", "bob")
	err = svc.ImportAppend(other, sessionID, 0, []byte("x"))
	if !errors.As(err, new(*ImportSessionOwnerError)) {
		t.Fatalf("error = %T (%v), want *ImportSessionOwnerError", err, err)
	}
}

func TestImportAppend_CrossTenantRefused(t *testing.T) {
	host := newImportSessionHost()
	svc := NewBatchService(host, 1)
	begun := withCaller(context.Background(), "acme", "alice")

	sessionID, err := svc.ImportBegin(begun, ImportOptions{Format: "csv", KeyField: "id", ValueMode: "row"})
	if err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}

	// Different tenant, identity shape otherwise identical -- a SEPARATE
	// case from the wrong-caller test: either check alone could be
	// vacuous in a way only the other catches.
	other := withCaller(context.Background(), "other-tenant", "alice")
	err = svc.ImportAppend(other, sessionID, 0, []byte("x"))
	if !errors.As(err, new(*ImportSessionOwnerError)) {
		t.Fatalf("error = %T (%v), want *ImportSessionOwnerError", err, err)
	}
}

func TestImportAbort_DiscardsStagedData(t *testing.T) {
	host := newImportSessionHost()
	svc := NewBatchService(host, 1)
	ctx := withCaller(context.Background(), "acme", "alice")

	sessionID, err := svc.ImportBegin(ctx, ImportOptions{Format: "csv", KeyField: "id", ValueMode: "row"})
	if err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}
	if err := svc.ImportAppend(ctx, sessionID, 0, []byte("id\nk1\n")); err != nil {
		t.Fatalf("ImportAppend: %v", err)
	}
	if err := svc.ImportAbort(ctx, sessionID); err != nil {
		t.Fatalf("ImportAbort: %v", err)
	}

	// Nothing survives an abort: a subsequent append, commit, or abort
	// all report the session gone.
	if err := svc.ImportAppend(ctx, sessionID, 1, []byte("x")); !errors.As(err, new(*ImportSessionNotFoundError)) {
		t.Errorf("ImportAppend after abort error = %T, want *ImportSessionNotFoundError", err)
	}
	if _, err := svc.ImportCommit(ctx, sessionID); !errors.As(err, new(*ImportSessionNotFoundError)) {
		t.Errorf("ImportCommit after abort error = %T, want *ImportSessionNotFoundError", err)
	}
}

func TestImportAbort_UnknownSessionRefused(t *testing.T) {
	host := newImportSessionHost()
	svc := NewBatchService(host, 1)
	ctx := withCaller(context.Background(), "acme", "alice")

	err := svc.ImportAbort(ctx, "no-such-session")
	if !errors.As(err, new(*ImportSessionNotFoundError)) {
		t.Fatalf("error = %T (%v), want *ImportSessionNotFoundError", err, err)
	}
}

func TestImportBegin_SessionsPerTenantQuota(t *testing.T) {
	host := newImportSessionHost()
	svc := NewBatchService(host, 1, WithImportSessionLimits(0, 0, 1, 0))
	ctx := withCaller(context.Background(), "acme", "alice")

	if _, err := svc.ImportBegin(ctx, ImportOptions{Format: "csv", KeyField: "id", ValueMode: "row"}); err != nil {
		t.Fatalf("first ImportBegin: %v", err)
	}

	_, err := svc.ImportBegin(ctx, ImportOptions{Format: "csv", KeyField: "id", ValueMode: "row"})
	if !errors.As(err, new(*ImportQuotaExceededError)) {
		t.Fatalf("error = %T (%v), want *ImportQuotaExceededError", err, err)
	}
}

func TestImportAppend_SessionBytesQuota(t *testing.T) {
	host := newImportSessionHost()
	svc := NewBatchService(host, 1, WithImportSessionLimits(4, 0, 0, 0))
	ctx := withCaller(context.Background(), "acme", "alice")

	sessionID, err := svc.ImportBegin(ctx, ImportOptions{Format: "csv", KeyField: "id", ValueMode: "row"})
	if err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}

	err = svc.ImportAppend(ctx, sessionID, 0, []byte("12345"))
	if !errors.As(err, new(*ImportQuotaExceededError)) {
		t.Fatalf("error = %T (%v), want *ImportQuotaExceededError", err, err)
	}
}

func TestImportAppend_ChunkCountQuota(t *testing.T) {
	host := newImportSessionHost()
	svc := NewBatchService(host, 1, WithImportSessionLimits(0, 1, 0, 0))
	ctx := withCaller(context.Background(), "acme", "alice")

	sessionID, err := svc.ImportBegin(ctx, ImportOptions{Format: "csv", KeyField: "id", ValueMode: "row"})
	if err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}
	if err := svc.ImportAppend(ctx, sessionID, 0, []byte("a")); err != nil {
		t.Fatalf("first ImportAppend: %v", err)
	}
	err = svc.ImportAppend(ctx, sessionID, 1, []byte("b"))
	if !errors.As(err, new(*ImportQuotaExceededError)) {
		t.Fatalf("error = %T (%v), want *ImportQuotaExceededError", err, err)
	}
}

func TestImportCommit_FailureLeavesSessionIntact(t *testing.T) {
	host := newImportSessionHost()
	svc := NewBatchService(host, 1)
	ctx := withCaller(context.Background(), "acme", "alice")

	// An invalid format at Begin-validation time would be refused before
	// any staging happened; instead force the parse itself to fail by
	// staging an archive format whose bytes are not a valid archive.
	sessionID, err := svc.ImportBegin(ctx, ImportOptions{Format: "archive", KeyField: "id", ValueMode: "row"})
	if err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}
	if err := svc.ImportAppend(ctx, sessionID, 0, []byte("not a real archive")); err != nil {
		t.Fatalf("ImportAppend: %v", err)
	}

	if _, err := svc.ImportCommit(ctx, sessionID); err == nil {
		t.Fatal("expected ImportCommit to fail parsing an invalid archive")
	}

	// The session must still be there: Abort must succeed, which it
	// could not if Commit had already released it despite failing.
	if err := svc.ImportAbort(ctx, sessionID); err != nil {
		t.Fatalf("ImportAbort after a failed commit: %v", err)
	}
}

// bigNDJSONRow returns one NDJSON line whose "row" JSON-encoded value is
// well over 1 MiB, so a handful of them exceed maxImportProposalBytes
// (8 MiB) and force proposeImportOps to split the import into more than one
// Raft proposal (buildImportProposalChunks).
func bigNDJSONRow(id string) string {
	pad := strings.Repeat("x", 3<<20) // ~3 MiB
	return `{"id":"` + id + `","pad":"` + pad + `"}`
}

// TestImportCommit_ProposalFaultMidImport_LeavesSessionRetryable is a
// fault-injection test for go-qrdb#576's security review finding 4: a
// multi-chunk import whose SECOND Raft proposal suffers an outcome-unknown
// fault (the chunk may or may not have actually committed -- see
// service.ErrOutcomeUnknown) must not report overall success and must not
// revoke the session. Before the fix, proposeImportOps treated ANY chunk
// error the same as a clean, provably-nothing-stored refusal: it recorded
// the chunk's rows as failed and continued, so ImportCommit returned
// (result, nil) -- success -- and revoked the session, destroying the only
// copy of whichever chunk's bytes were never resolved.
func TestImportCommit_ProposalFaultMidImport_LeavesSessionRetryable(t *testing.T) {
	host, st := newImportSessionHostWithStore()
	svc := NewBatchService(host, 1)
	ctx := withCaller(context.Background(), "acme", "alice")

	sessionID, err := svc.ImportBegin(ctx, ImportOptions{Format: "ndjson", KeyField: "id", ValueMode: "row"})
	if err != nil {
		t.Fatalf("ImportBegin: %v", err)
	}

	// Three ~3 MiB rows: the first two chunk together under the 8 MiB
	// ceiling, forcing the third into a SECOND proposal.
	data := bigNDJSONRow("k1") + "\n" + bigNDJSONRow("k2") + "\n" + bigNDJSONRow("k3") + "\n"
	if err := svc.ImportAppend(ctx, sessionID, 0, []byte(data)); err != nil {
		t.Fatalf("ImportAppend: %v", err)
	}

	// Inject the fault on the second OpBatch proposal only; the first
	// commits normally through the real fake-store logic.
	batchCalls := 0
	real := host.syncProposeFn
	host.syncProposeFn = func(ctx context.Context, shardID uint64, cmd []byte) (sm.Result, error) {
		decoded, derr := protocol.DecodeCommand(cmd)
		if derr == nil && decoded.Op == protocol.OpBatch {
			batchCalls++
			if batchCalls == 2 {
				return sm.Result{}, errors.New("simulated network fault")
			}
		}
		return real(ctx, shardID, cmd)
	}

	_, commitErr := svc.ImportCommit(ctx, sessionID)
	if commitErr == nil {
		t.Fatal("expected ImportCommit to fail on the injected mid-import fault")
	}
	if !errors.Is(commitErr, ErrOutcomeUnknown) {
		t.Errorf("ImportCommit error = %v, want errors.Is(err, ErrOutcomeUnknown)", commitErr)
	}
	if batchCalls < 2 {
		t.Fatalf("only %d OpBatch proposal(s) were issued; the fault was never exercised", batchCalls)
	}

	// The session must still be there -- Abort must succeed, which it could
	// not if Commit had already revoked it despite reporting an error.
	if err := svc.ImportAbort(ctx, sessionID); err != nil {
		t.Fatalf("ImportAbort after a fault-injected commit: %v", err)
	}

	// Nothing from the SECOND (faulted) chunk's row is visible: whatever the
	// first chunk's rows wrote stays exactly as committed, uninflated by any
	// row this call never resolved.
	if st.has("k3") {
		t.Error("k3 (the row in the faulted chunk) was written despite the fault")
	}
}
