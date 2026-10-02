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

// Package compaction defines scheduled compaction's policy and its replicated
// state: the schedule on which a database's MVCC history -- or, set by the
// platform, the node keyspace's -- is compacted, and the reserved record that
// holds it in the store holding that data (State, RecordKey). It imports only
// the leaf pkg/tenantid, so the state machine, the services and every
// transport share one definition and one validation. See
// docs/key-value/engineering.md "Scheduled compaction".
package compaction

import "time"

// Mode selects a policy.
type Mode string

const (
	// Off is the default: the database is never compacted except by an
	// explicit Compact from its owning tenant. An empty mode means off.
	Off Mode = "off"

	// Revision keeps the last RetainRevisions revisions of the store serving
	// the database.
	Revision Mode = "revision"

	// Period keeps the history newer than RetainPeriod.
	Period Mode = "period"
)

// MinRetainPeriod is the shortest RetainPeriod a period policy accepts.
const MinRetainPeriod = time.Second

// Policy is a database's scheduled-compaction policy. A nil policy, or one
// whose mode is off, compacts nothing.
type Policy struct {
	// Mode is off (or empty), revision or period.
	Mode Mode `yaml:"mode" json:"mode,omitempty"`

	// RetainRevisions is how many of the store's most recent revisions a
	// revision policy keeps. Revision mode only; at least 1.
	RetainRevisions int64 `yaml:"retain_revisions" json:"retainRevisions,omitempty"`

	// RetainPeriod is how much history, by age, a period policy keeps: a Go
	// duration of at least MinRetainPeriod. Period mode only.
	RetainPeriod string `yaml:"retain_period" json:"retainPeriod,omitempty"`
}

// validators checks a policy's fields for its mode. A map rather than a
// switch: the modes are values and the dispatch is a lookup, and a mode with
// no row is an unknown mode.
var validators = map[Mode]func(*Policy) error{
	"":       validateOff,
	Off:      validateOff,
	Revision: validateRevision,
	Period:   validatePeriod,
}

// Validate refuses an unknown mode, a field that does not belong to the mode
// -- which would otherwise be silently ignored -- and an out-of-range
// retention.
func (p *Policy) Validate() error {
	validate, ok := validators[p.Mode]
	if !ok {
		return &InvalidPolicyError{Reason: "unknown mode " + string(p.Mode) + " (want off, revision or period)"}
	}
	return validate(p)
}

func validateOff(p *Policy) error {
	if p.RetainRevisions != 0 || p.RetainPeriod != "" {
		return &InvalidPolicyError{Reason: "a policy that is off takes no retention"}
	}
	return nil
}

func validateRevision(p *Policy) error {
	if p.RetainPeriod != "" {
		return &InvalidPolicyError{Reason: "retain_period belongs to period mode"}
	}
	if p.RetainRevisions < 1 {
		return &InvalidPolicyError{Reason: "revision mode needs retain_revisions of at least 1"}
	}
	return nil
}

func validatePeriod(p *Policy) error {
	if p.RetainRevisions != 0 {
		return &InvalidPolicyError{Reason: "retain_revisions belongs to revision mode"}
	}
	d, err := time.ParseDuration(p.RetainPeriod)
	if err != nil {
		return &InvalidPolicyError{Reason: "retain_period " + p.RetainPeriod + " is not a duration"}
	}
	if d < MinRetainPeriod {
		return &InvalidPolicyError{Reason: "retain_period must be at least " + MinRetainPeriod.String()}
	}
	return nil
}

// Enabled reports whether p compacts anything: a valid policy whose mode is
// revision or period.
func (p *Policy) Enabled() bool {
	if p == nil || p.Validate() != nil {
		return false
	}
	return p.Mode == Revision || p.Mode == Period
}

// RetainDuration returns a period policy's retention, and zero for any other
// policy.
func (p *Policy) RetainDuration() time.Duration {
	if p == nil || p.Mode != Period {
		return 0
	}
	d, err := time.ParseDuration(p.RetainPeriod)
	if err != nil {
		return 0
	}
	return d
}

// Normalized returns the policy as stored: nil for a policy that is off, so
// "off" and "never set" are one state.
func (p *Policy) Normalized() *Policy {
	if p == nil || p.Mode == "" || p.Mode == Off {
		return nil
	}
	cp := *p
	return &cp
}

// InvalidPolicyError is returned for a policy with an unknown mode, a field
// that does not belong to its mode, or an out-of-range retention. See
// Policy.Validate.
type InvalidPolicyError struct {
	Reason string
}

// Error returns a human-readable description of the invalid policy.
func (e *InvalidPolicyError) Error() string {
	return "compaction: invalid policy: " + e.Reason
}
