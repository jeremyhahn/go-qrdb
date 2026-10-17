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

package store

import (
	"github.com/jeremyhahn/go-qrdb/pkg/compaction"
	"github.com/jeremyhahn/go-qrdb/pkg/importstage"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// metaKeyLastApplied is the reserved key used to persist the last applied
// Raft log index for crash recovery. Uses a null prefix to sort before user data.
const metaKeyLastApplied = "\x00__quicraft_last_applied__"

// isNodeLocalKey reports whether k is a record local to this node -- the
// last-applied index -- which a snapshot must not carry and a replica hash
// must not cover, because every replica holds its own value for it.
//
// Both state machines reserve the 0x00 prefix for their own records. All but
// four of those are node-local. The four replicated tables are written only
// in the apply batch, so they travel in snapshots (and so in backups) and are
// covered by the hash like any other applied record:
//
//   - the lease table (lease.TablePrefix): a snapshot that dropped it would
//     hand the recovering replica keys bound to leases it has never heard
//     of, which then never expire;
//   - the compaction table (compaction.TablePrefix): a snapshot that dropped
//     it would leave the recovering replica -- or a restored cluster -- with
//     no compaction policy, and a new leader with no samples, so its
//     schedule would silently stop or restart its retention clock;
//   - the replicated audit table (vlog.TablePrefix): a snapshot that dropped
//     it would hand the recovering replica a chain that resumes from a head
//     no committed log entry produced, breaking the property
//     (docs/audit/architecture.md#the-hash-chain-stays-linear-under-raft-because-it-is-assigned-at-apply-time)
//     this table exists to guarantee;
//   - the import staging table (importstage.TablePrefix): a snapshot that
//     dropped it would hand the recovering replica lease bindings for staged
//     chunks it does not hold, so a chunked import resumed against it after a
//     leader change would find the chunks staged before the snapshot missing.
func isNodeLocalKey(k []byte) bool {
	return len(k) > 0 && k[0] == '\x00' &&
		!lease.IsTableKey(k) && !compaction.IsTableKey(k) && !vlog.IsTableKey(k) && !importstage.IsTableKey(k)
}

// The internal key namespace is declared once, in pkg/service. This package
// delegates both halves of the guard to it — the predicate and the skip rule —
// so a prefix added there is honoured here automatically and the two can never
// be answered from different lists.

// isInternalKey reports whether k belongs to an internal subsystem's reserved
// namespace and must therefore be excluded from a user-facing key listing.
func isInternalKey(k string) bool {
	return service.IsInternalKey(k)
}

// skipInternalKeyFilter reports whether an enumeration rooted at prefix is the
// owning subsystem reading its own reserved namespace back, in which case the
// filter must not be applied or the subsystem would be hidden from its own
// records.
func skipInternalKeyFilter(prefix string) bool {
	return service.SkipInternalKeyFilter(prefix)
}
