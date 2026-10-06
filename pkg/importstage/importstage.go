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

// Package importstage defines the reserved, replicated key layout a
// chunked import session's metadata and staged chunks live under:
//
//	TablePrefix + "s/" + sessionID + "/meta"            -> session metadata, JSON
//	TablePrefix + "s/" + sessionID + "/c/" + seq (8 BE)  -> one staged chunk
//
// Every record is written only through protocol.OpImportStage, directly
// into the state machine's raw keyspace -- never through the MVCC store or
// the KV engine's own Put path -- exactly like the lease table
// (pkg/lease) and the compaction table (pkg/compaction). pkg/mvcc's own key
// encoding (EncodeMetaKey, EncodeDataKey) never produces the literal key a
// client's Get/Range/Put names, so a record staged here is structurally
// unreachable from the client data plane: this is what closes go-qrdb#576's
// security review finding 3 (a discovery-only guard is not storage
// isolation). Every write also binds its key to the session's lease
// (pkg/lease.Lessor.Bind), so revoking or expiring that lease deletes the
// record through the SAME cascade an ordinary lease-bound key already
// gets, with no separate cleanup path to keep correct.
//
// The prefix begins with 0x00, the byte the state machines reserve for
// their own records (see pkg/lease and pkg/compaction's identical
// convention), so it sorts ahead of every user key and is never one.
package importstage

import (
	"bytes"
	"encoding/binary"
)

// TablePrefix is the raw state-machine key prefix import session records are
// stored under.
const TablePrefix = "\x00qrdb.import/"

// sessionPrefix is the record family every session's keys share.
const sessionPrefix = TablePrefix + "s/"

// metaSuffix terminates a session's metadata key, distinguishing it from a
// chunk key when scanning for open sessions.
const metaSuffix = "/meta"

// chunkInfix separates a session id from its chunk sequence number.
const chunkInfix = "/c/"

// IsTableKey reports whether k is an import-session record.
func IsTableKey(k []byte) bool {
	return bytes.HasPrefix(k, []byte(TablePrefix))
}

// MetaKey returns the key of sessionID's metadata record.
func MetaKey(sessionID string) []byte {
	return []byte(sessionPrefix + sessionID + metaSuffix)
}

// ChunkKey returns the key of sessionID's chunk seq.
func ChunkKey(sessionID string, seq uint64) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, seq)
	return append([]byte(sessionPrefix+sessionID+chunkInfix), buf...)
}

// tenantIndexPrefix is the record family a tenant's open sessions are
// indexed under, independent of which database (or none) each session's
// own id is namespaced to -- see TenantIndexKey.
const tenantIndexPrefix = TablePrefix + "t/"

// TenantIndexKey returns the key of tenantID's index entry for sessionID:
// an empty marker written (and lease-bound) in the SAME apply call as the
// session's metadata record, existing only so TenantIndexScanPrefix can
// count a tenant's open sessions by a single prefix scan regardless of
// whether the session's own id carries a database namespace at all --
// ImportBegin's per-tenant quota is a property of the CALLER, not of
// whichever database namespace (if any) a particular import happens to
// target.
func TenantIndexKey(tenantID, sessionID string) []byte {
	return []byte(tenantIndexPrefix + tenantID + "/" + sessionID)
}

// TenantIndexScanPrefix returns the key range every one of tenantID's open
// sessions is indexed under.
func TenantIndexScanPrefix(tenantID string) []byte {
	return []byte(tenantIndexPrefix + tenantID + "/")
}
