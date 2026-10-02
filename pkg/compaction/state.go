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

package compaction

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// The compaction table holds each scope's scheduled-compaction state -- its
// policy and the revision samples a period policy targets from -- as one
// reserved record in the store that holds the scope's data:
//
//	TablePrefix + scope  ->  State, JSON
//
// A scope is a database's "<tenant>/<db>/" namespace on a store many
// databases share, "" on a store that is one database's own (a dedicated raft
// group), or NodeScope for the node keyspace of the shared store.
//
// The prefix begins with 0x00, the byte the state machines reserve for their
// own records, so it sorts ahead of every user key and is never a user key.
// Like the lease table it is replicated state: it is written only in the
// apply batch, by the compaction-state command, so every replica holds the
// same records, snapshots (and so backups) carry them, and the replica hash
// covers them. There is no node-local copy anywhere.

// TablePrefix is the raw state-machine key prefix the compaction table is
// stored under.
const TablePrefix = "\x00qrdb.compaction/"

// NodeScope is the scope of the node keyspace -- every key outside all
// database namespaces -- on the shared store. A NUL appears in no database
// namespace, so it can be no database's scope.
const NodeScope = "\x00node"

// SampleResolution is how many revision samples a period policy keeps per
// period: a sample is recorded only once the newest is period/SampleResolution
// old, so a scope holds about this many however often it is sampled, and its
// target is at most one sample interval older than the period -- precision
// lost on the side of keeping history.
const SampleResolution = 64

// IsTableKey reports whether k is a compaction-table record.
func IsTableKey(k []byte) bool {
	return bytes.HasPrefix(k, []byte(TablePrefix))
}

// RecordKey returns the key of scope's record.
func RecordKey(scope string) []byte {
	return []byte(TablePrefix + scope)
}

// ScopeOf returns the scope a compaction-table key belongs to.
func ScopeOf(k []byte) (string, bool) {
	if !IsTableKey(k) {
		return "", false
	}
	return string(k[len(TablePrefix):]), true
}

// ValidScope reports whether scope can hold a record: "", NodeScope, or
// exactly one "<tenant>/<db>/" namespace. Any other prefix could span several
// databases or part of one.
func ValidScope(scope string) bool {
	if scope == "" || scope == NodeScope {
		return true
	}
	ns, ok := tenantid.NamespaceOf([]byte(scope))
	return ok && len(ns) == len(scope)
}

// Sample is a store's current revision as observed at a time.
type Sample struct {
	// At is the observation time, Unix nanoseconds.
	At int64 `json:"at"`
	// Revision is the store's current revision at At.
	Revision int64 `json:"revision"`
}

// Binding is what setting a policy writes: the policy and the database
// incarnation it belongs to. Tenant and Database name the database whose drop
// fence (service.DropFenceGuard) guards every compaction and sample the
// schedule proposes, and Incarnation is the incarnation that guard demands --
// so a record that outlives its database, or one a re-created database of the
// same name never wrote, can compact nothing: the state machine refuses its
// proposals. All three are empty for the node keyspace, which no database
// owns, and Incarnation is empty for a database whose store is its own (a
// dedicated raft group: the group is the incarnation).
type Binding struct {
	// Policy is the policy; nil is off.
	Policy *Policy `json:"policy,omitempty"`
	// Tenant owns the database.
	Tenant string `json:"tenant,omitempty"`
	// Database is the database's name.
	Database string `json:"database,omitempty"`
	// Incarnation is the database's incarnation.
	Incarnation string `json:"incarnation,omitempty"`
}

// State is one scope's scheduled-compaction state.
type State struct {
	// Policy is the scope's policy; nil is off.
	Policy *Policy `json:"policy,omitempty"`
	// Tenant, Database and Incarnation are the Binding the policy was set
	// with.
	Tenant      string `json:"tenant,omitempty"`
	Database    string `json:"database,omitempty"`
	Incarnation string `json:"incarnation,omitempty"`
	// Samples are a period policy's revision observations, oldest first.
	Samples []Sample `json:"samples,omitempty"`
}

// Bind sets the state's policy from b. A binding for another database
// incarnation replaces the state outright -- the samples observed one
// incarnation's revisions -- and a policy that does not sample keeps none.
func (s *State) Bind(b Binding) {
	if s.Tenant != b.Tenant || s.Database != b.Database || s.Incarnation != b.Incarnation {
		*s = State{}
	}
	s.Policy, s.Tenant, s.Database, s.Incarnation = b.Policy, b.Tenant, b.Database, b.Incarnation
	if b.Policy == nil || b.Policy.Mode != Period {
		s.Samples = nil
	}
}

// Encode returns the state's record value.
func (s *State) Encode() ([]byte, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return nil, &CorruptStateError{Err: err}
	}
	return data, nil
}

// DecodeState decodes a record value.
func DecodeState(data []byte) (*State, error) {
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, &CorruptStateError{Err: err}
	}
	return &s, nil
}

// Record adds the sample (at, revision) to a period policy's samples and
// reports whether it was kept. It is deterministic in its inputs, which the
// state machine needs: the time travels in the command, not from a clock.
//
//   - Only a period policy samples; any other state keeps nothing.
//   - A sample no newer than the newest is ignored: a delayed or repeated
//     proposal, from this leader or a former one, changes nothing.
//   - A newer sample whose revision is below the newest sample's means the
//     store's revisions started over (a restore, a re-created store): every
//     sample describes revisions this store no longer has, so they are all
//     dropped and the new one starts the series.
//   - Otherwise a sample is kept only once the newest is a sample interval
//     (period/SampleResolution) old.
//   - Samples older than the newest one at least a period before at are
//     dropped: the target never needs them again.
func (s *State) Record(at, revision int64) bool {
	period := s.Policy.RetainDuration()
	if period <= 0 {
		return false
	}
	if n := len(s.Samples); n > 0 {
		newest := s.Samples[n-1]
		switch {
		case at <= newest.At:
			return false
		case revision < newest.Revision:
			s.Samples = nil
		case time.Duration(at-newest.At) < period/SampleResolution:
			return false
		}
	}
	s.Samples = append(s.Samples, Sample{At: at, Revision: revision})
	if keep := newestAtOrBefore(s.Samples, at-int64(period)); keep > 0 {
		s.Samples = append([]Sample(nil), s.Samples[keep:]...)
	}
	return true
}

// Target returns the revision the policy compacts to at now, with current
// the store's current revision, or a value below 1 when there is nothing to
// compact yet: current minus RetainRevisions for a revision policy; for a
// period policy the newest usable sample at least the period old -- none yet,
// after a fresh policy, means nothing is compacted. Samples that cannot
// describe this store are not usable (UsableSamples), so they never become a
// target.
func (s *State) Target(now time.Time, current int64) int64 {
	if !s.Policy.Enabled() {
		return 0
	}
	if s.Policy.Mode == Revision {
		return current - s.Policy.RetainRevisions
	}
	samples := s.UsableSamples(now, current)
	i := newestAtOrBefore(samples, now.Add(-s.Policy.RetainDuration()).UnixNano())
	if i < 0 {
		return 0
	}
	return samples[i].Revision
}

// UsableSamples returns the samples that can describe this store at now, with
// current its current revision -- none at all when any of them cannot, since
// one bad observation puts the whole series in doubt:
//
//   - a sample stamped after now came from a clock this node does not share;
//   - samples out of order in time or revision were not written by
//     Record;
//   - a sample above current observed revisions this store does not have
//     (its revisions started over).
//
// An empty answer errs toward keeping history: a period policy compacts
// nothing until a full period has been observed again.
func (s *State) UsableSamples(now time.Time, current int64) []Sample {
	for i, smp := range s.Samples {
		if smp.At > now.UnixNano() || smp.Revision > current {
			return nil
		}
		if i > 0 && (smp.At <= s.Samples[i-1].At || smp.Revision < s.Samples[i-1].Revision) {
			return nil
		}
	}
	return s.Samples
}

// newestAtOrBefore returns the index of the newest sample observed at or
// before cutoff, or -1 when there is none.
func newestAtOrBefore(samples []Sample, cutoff int64) int {
	newest := -1
	for i, smp := range samples {
		if smp.At > cutoff {
			break
		}
		newest = i
	}
	return newest
}

// CorruptStateError reports a compaction-table record that cannot be encoded
// or decoded.
type CorruptStateError struct {
	// Err is the cause.
	Err error
}

// Error returns a human-readable description.
func (e *CorruptStateError) Error() string {
	return "compaction: corrupt state record: " + e.Err.Error()
}

// Unwrap returns the cause.
func (e *CorruptStateError) Unwrap() error { return e.Err }
