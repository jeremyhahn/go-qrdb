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

package statemachine

import (
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/importstage"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// Chunked import session staging for every KVStateMachine.
//
// A session's metadata and staged chunks (pkg/importstage) are written
// directly into k.backend through ApplyAtomic, bound to the session's
// lease exactly as any other raw-keyspace write is (commitLeased's own
// pattern) -- so revoking or expiring that lease deletes them through
// handleLeaseRevoke's ordinary cascade, with no separate cleanup path.
// This is the same raw-keyspace treatment the lease table itself gets
// (kv_lease.go), which is what makes it reachable from no client
// Get/Put/Range verb: validateClientKey refuses a client's own write of a
// key under importstage.TablePrefix (see isStateMachineKey), and this
// handler is reached only through protocol.OpImportStage, which no client
// request encodes.

// handleImportStage writes a session's metadata or one staged chunk.
func (k *KVStateMachine) handleImportStage(payload []byte, _ uint64) (sm.Result, error) {
	cmd, err := protocol.DecodeImportStage(payload)
	if err != nil {
		return sm.Result{}, &IncompleteDataError{Reason: "import stage: " + err.Error()}
	}
	// A staged record with no lease would never be reclaimed, so NoLease is
	// refused here rather than treated as "no lease named" the way an
	// ordinary put's optional lease is.
	if cmd.LeaseID == lease.NoLease {
		return sm.Result{Data: protocol.EncodeLeaseNotFoundError(cmd.LeaseID)}, nil
	}
	if res, refused := LeaseRefusal(k.lessor, cmd.LeaseID); refused {
		return res, nil
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
			open, err := k.countOpenImportSessions(cmd.TenantID)
			if err != nil {
				return sm.Result{}, err
			}
			if open >= int(cmd.MaxSessions) {
				return sm.Result{Data: protocol.EncodeAppError(protocol.AppErrorConflict, "import session quota exceeded")}, nil
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
		return sm.Result{}, &IncompleteDataError{Reason: "import stage: unknown kind"}
	}

	muts := make([]Mutation, len(writes))
	leases := make([]int64, len(writes))
	for i, w := range writes {
		muts[i] = Mutation{Key: w.key, Value: w.value}
		leases[i] = cmd.LeaseID
	}
	if err := k.commitLeased(muts, leases); err != nil {
		return sm.Result{}, &StorageError{Op: "import_stage", Inner: err}
	}
	return sm.Result{Value: 1}, nil
}

// lookupImportStage answers the import-staging read queries.
func (k *KVStateMachine) lookupImportStage(query interface{}) (interface{}, bool, error) {
	switch q := query.(type) {
	case ImportStageMetaQuery:
		v, err := k.backend.Get(importstage.MetaKey(q.SessionID))
		return v, true, err
	case ImportStageChunkQuery:
		v, err := k.backend.Get(importstage.ChunkKey(q.SessionID, q.Seq))
		return v, true, err
	case ImportStageOpenSessionsQuery:
		n, err := k.countOpenImportSessions(q.TenantID)
		return n, true, err
	default:
		return nil, false, nil
	}
}

// countOpenImportSessions counts tenantID's open sessions via its tenant
// index (pkg/importstage.TenantIndexKey), reading only keys (Backend.List,
// never Scan) so the cost is independent of how many bytes those sessions
// have staged.
func (k *KVStateMachine) countOpenImportSessions(tenantID string) (int, error) {
	keys, err := k.backend.List(importstage.TenantIndexScanPrefix(tenantID))
	if err != nil {
		return 0, err
	}
	return len(keys), nil
}
