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
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// metaKeyLastApplied is the reserved key used to persist the last applied
// Raft log index for crash recovery. Uses a null prefix to sort before user data.
const metaKeyLastApplied = "\x00__quicraft_last_applied__"

// isNodeLocalKey reports whether k is a record local to this node -- the
// last-applied index -- which a snapshot must not carry and a replica hash
// must not cover, because every replica holds its own value for it.
//
// Both state machines reserve the 0x00 prefix for their own records. All but
// one of those are node-local; the lease table (lease.TablePrefix) is
// replicated state, written in the apply batch alongside the data it
// governs, so it travels in snapshots and is covered by the hash like any
// other applied record. A snapshot that dropped it would hand the recovering
// replica keys bound to leases it has never heard of, which then never
// expire.
func isNodeLocalKey(k []byte) bool {
	return len(k) > 0 && k[0] == '\x00' && !lease.IsTableKey(k)
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
