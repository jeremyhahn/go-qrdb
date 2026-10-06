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

package engine

import (
	"errors"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/importstage"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/mvcc"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// handleImportStage processes an OpImportStage command: it writes a chunked
// import session's metadata or one staged chunk directly into the state
// machine's reserved keyspace via batch.Set, exactly like the lease table
// and the compaction table (handleCompactionState) -- never through
// s *mvcc.Store (unused here), so the record is never an MVCC key and is
// structurally unreachable from a client Get/Range/Put of the same literal
// key (pkg/mvcc's own key encoding never produces it). The write is bound to
// the session's lease (bindLease), so revoking or expiring that lease
// deletes the record through the ordinary lease cascade
// (handleLeaseRevoke), with no separate cleanup path.
//
// A lease id of lease.NoLease, or one the lessor does not hold, is a
// deterministic consequence of the caller's own request (the session's
// lease was never granted, or already expired) and is refused in the result
// rather than halting apply.
func handleImportStage(_ *mvcc.Store, lr *lease.Lessor, batch storage.Batch, payload []byte) (sm.Result, []Notification, error) {
	cmd, err := protocol.DecodeImportStage(payload)
	if err != nil {
		return sm.Result{}, nil, err
	}
	if lr == nil {
		return statemachine.RefusalResult(&statemachine.ServiceNotConfiguredError{Service: "lease"}), nil, nil
	}
	if cmd.LeaseID == lease.NoLease || !lr.Has(cmd.LeaseID) {
		return statemachine.RefusalResult(&service.LeaseNotFoundError{LeaseID: cmd.LeaseID}), nil, nil
	}

	type write struct {
		key   []byte
		value []byte
	}
	var writes []write
	switch cmd.Kind {
	case protocol.ImportStageSetMeta:
		// Count and write commit at the SAME log position: this Update()
		// call is the only writer Raft ever runs concurrently with itself,
		// so two ImportBegin proposals can never both observe room for the
		// last slot of MaxSessions (go-qrdb#576 security review).
		if cmd.MaxSessions > 0 {
			open, err := countOpenImportSessions(batch, cmd.TenantID)
			if err != nil {
				return sm.Result{}, nil, err
			}
			if open >= int(cmd.MaxSessions) {
				return sm.Result{Data: protocol.EncodeAppError(protocol.AppErrorConflict, "import session quota exceeded")}, nil, nil
			}
		}
		writes = []write{
			{importstage.MetaKey(cmd.SessionID), cmd.Body},
			// The tenant-index entry commits in the SAME ApplyAtomic set
			// as the metadata record, and is bound to the SAME lease, so
			// it is counted and reclaimed exactly in step with the
			// session it indexes -- see countOpenImportSessions and
			// pkg/importstage.TenantIndexKey.
			{importstage.TenantIndexKey(cmd.TenantID, cmd.SessionID), nil},
		}
	case protocol.ImportStageSetChunk:
		writes = []write{{importstage.ChunkKey(cmd.SessionID, cmd.Seq), cmd.Body}}
		if len(cmd.MetaBody) > 0 {
			// The chunk and the metadata that now accounts for it commit
			// in the SAME ApplyAtomic set, exactly as putRawBatch
			// committed them as one Raft command before this package
			// existed.
			writes = append(writes, write{importstage.MetaKey(cmd.SessionID), cmd.MetaBody})
		}
	default:
		return sm.Result{}, nil, &protocol.InvalidCommandError{Reason: "import stage: unknown kind"}
	}

	for _, w := range writes {
		if err := batch.Set(w.key, w.value); err != nil {
			return sm.Result{}, nil, err
		}
		if err := bindLease(lr, batch, w.key, cmd.LeaseID); err != nil {
			var twe *lease.TableWriteError
			if errors.As(err, &twe) {
				return sm.Result{}, nil, err
			}
			return statemachine.RefusalResult(err), nil, nil
		}
	}
	return sm.Result{Value: 1}, nil, nil
}

// countOpenImportSessions counts tenantID's open sessions via its tenant
// index (pkg/importstage.TenantIndexKey), reading ONLY keys
// (storage.IteratorOptions.OnlyKeys) so the cost is independent of how many
// bytes those sessions have staged -- go-qrdb#576's security review
// measured the previous MVCC-range implementation, which read every
// chunk's value, at up to ~1 GiB per call at default limits.
func countOpenImportSessions(r storage.Reader, tenantID string) (int, error) {
	prefix := importstage.TenantIndexScanPrefix(tenantID)
	iter := r.NewIterator(&storage.IteratorOptions{
		LowerBound: prefix,
		UpperBound: prefixEndBytes(prefix),
		OnlyKeys:   true,
	})
	defer func() { _ = iter.Close() }()

	count := 0
	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		count++
	}
	return count, iter.Error()
}
