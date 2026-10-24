// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package wiring

import (
	"context"
	"crypto/fips140"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// SeparationPostureKey is the replicated system record holding the
// separation-of-duties posture the cluster last ran under: the bytes
// PostureStrict or PostureStandard. One record per cluster, beside
// __sys:cluster/isolation_policy.
const SeparationPostureKey = "__sys:cluster/separation_of_duties"

// The values of the SeparationPostureKey record, and of the "previous" and
// "posture" attributes of a posture-change audit event.
const (
	// PostureStrict is strict separation of duties.
	PostureStrict = "strict"
	// PostureStandard is the standard posture.
	PostureStandard = "standard"
	// PostureNone is the "previous" attribute of the event a cluster's first
	// recording emits: no posture was recorded before it.
	PostureNone = "none"
)

// SeparationPostureAuditComponent, SeparationPostureAuditResource and
// ActionSeparationPostureChange name the audit event RecordSeparationPosture
// emits.
const (
	SeparationPostureAuditComponent = "separation-of-duties"
	SeparationPostureAuditResource  = "auth/separation_of_duties"
	ActionSeparationPostureChange   = "posture-change"
)

// separationPostureAuditActor is the actor of a posture-change event: the
// node's own start, not a caller.
const separationPostureAuditActor = "system"

// separationPostureAttempts bounds the compare-and-set loop: each lost race
// means another node recorded a posture, which the next read observes.
const separationPostureAttempts = 3

// postureNames maps a resolved posture to its record value.
var postureNames = map[bool]string{true: PostureStrict, false: PostureStandard}

// SeparationPostureError reports that the recorded separation-of-duties
// posture could not be read or written.
type SeparationPostureError struct {
	// Op is the store operation that failed: "read", "create" or "swap", or
	// "contended" when every attempt lost its compare-and-set.
	Op string
	// Err is the store's error; nil for "contended".
	Err error
}

// Error implements error.
func (e *SeparationPostureError) Error() string {
	if e.Err == nil {
		return "auth/wiring: separation-of-duties posture record " + e.Op
	}
	return "auth/wiring: separation-of-duties posture record " + e.Op + ": " + e.Err.Error()
}

// Unwrap returns the store's error.
func (e *SeparationPostureError) Unwrap() error { return e.Err }

// RecordSeparationPosture compares the posture ResolveStrictSeparation gives
// for configured with the posture the cluster last recorded at
// SeparationPostureKey. When none is recorded (first boot) or the two differ,
// it records the new posture and emits one audit event to sink naming the
// previous posture, the new one and whether FIPS 140-3 mode forced it. An
// unchanged posture writes and emits nothing.
//
// The write is a compare-and-set on the existing replicated KV path, so of
// nodes starting together under the same change exactly one records it and
// emits its event. changed reports whether this call did. A nil sink records
// without emitting.
func RecordSeparationPosture(ctx context.Context, store kvstore.AtomicKVStore, sink audit.AuditLogger, configured bool) (changed bool, err error) {
	return recordSeparationPosture(ctx, store, sink, fips140.Enabled(), configured)
}

// recordSeparationPosture is RecordSeparationPosture over an explicit FIPS
// mode, the seam its tests drive.
func recordSeparationPosture(ctx context.Context, store kvstore.AtomicKVStore, sink audit.AuditLogger, fipsMode, configured bool) (bool, error) {
	next := postureNames[resolveStrictSeparation(fipsMode, configured)]
	for range separationPostureAttempts {
		prev, err := store.Get(ctx, SeparationPostureKey)
		if err != nil {
			return false, &SeparationPostureError{Op: "read", Err: err}
		}
		if string(prev) == next {
			return false, nil
		}
		won, err := writeSeparationPosture(ctx, store, prev, next)
		if err != nil {
			return false, err
		}
		if !won {
			continue
		}
		previous := PostureNone
		if prev != nil {
			previous = string(prev)
		}
		audit.EmitBestEffort(ctx, sink, SeparationPostureAuditComponent, audit.AuditEvent{
			Actor:    separationPostureAuditActor,
			Resource: SeparationPostureAuditResource,
			Action:   ActionSeparationPostureChange,
			Outcome:  "success",
			Attributes: map[string]any{
				"previous":    previous,
				"posture":     next,
				"fips_forced": fipsMode,
			},
		})
		return true, nil
	}
	return false, &SeparationPostureError{Op: "contended"}
}

// writeSeparationPosture creates the record when prev is nil and swaps it
// from prev otherwise, reporting whether this call's write won.
func writeSeparationPosture(ctx context.Context, store kvstore.AtomicKVStore, prev []byte, next string) (bool, error) {
	if prev == nil {
		stored, err := store.PutIfAbsent(ctx, SeparationPostureKey, []byte(next))
		if err != nil {
			return false, &SeparationPostureError{Op: "create", Err: err}
		}
		return stored, nil
	}
	swapped, err := store.CompareAndSwap(ctx, SeparationPostureKey, prev, []byte(next))
	if err != nil {
		return false, &SeparationPostureError{Op: "swap", Err: err}
	}
	return swapped, nil
}
