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

package server

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrTenantHostUnavailable is returned when a per-tenant volume Host is
// requested but the registry was not configured with a barrier registry and
// tenant metadata source.
var ErrTenantHostUnavailable = errors.New("server: tenant host registry not configured")

// ErrTenantDataRootUnset is returned when an IsolationVolume tenant has no
// DataRoot recorded in its metadata (it was never allocated from the pool).
var ErrTenantDataRootUnset = errors.New("server: tenant data root is unset")

// ErrTenantMigrationInProgress is returned by Services while a tenant's barrier
// is mid encryption-migration recycle. It is RETRYABLE: once the migration
// commits and replaces the barrier, the next call builds the Host under the
// keyed barrier. Building during the window would wrap the host with the stale
// source barrier and fail against the already-keyed at-rest stores.
var ErrTenantMigrationInProgress = errors.New("server: tenant encryption migration in progress")

// ErrTenantShardNoLeader is returned when a per-tenant Host's local shard fails
// to elect a leader within the readiness timeout.
var ErrTenantShardNoLeader = errors.New("server: tenant shard did not elect a leader")

// ShardLeaderWaitError reports that waitForShardLeader gave up, and -- unlike
// the bare ErrTenantShardNoLeader sentinel it wraps -- what it observed while
// waiting. It exists because five CI incidents (go-qrdb#139) each cost a manual
// diagnosis to answer one question the error itself should have answered: was
// the shard broken, or was the host too busy to let the poller ask?
//
// It wraps ErrTenantShardNoLeader, so every existing errors.Is check keeps
// working unchanged.
//
// The fields are the measurement, and Error() reports the verdict they support:
//
//	Polls    how many times the shard was actually asked
//	Elapsed  real wall time spent, which on a starved host greatly exceeds
//	         Polls * shardLeaderPollInterval
//	Interval the poll interval the wait used, which is the yardstick MaxGap is
//	         judged against
//	MaxGap   the longest interval between two consecutive polls -- the direct
//	         evidence of descheduling, since a 20ms sleep overruns by
//	         microseconds on an idle host
//	ErrCount how many polls GetLeaderID failed on, which separates an
//	         intermittent query failure from a persistent one -- different
//	         problems, different investigations
//	FirstErr the first non-nil GetLeaderID error, reported alongside the last
//	         when the two differ
//	LastErr  the last non-nil error GetLeaderID returned, which the previous
//	         implementation discarded
type ShardLeaderWaitError struct {
	ShardID  uint64
	Timeout  time.Duration
	Interval time.Duration
	Elapsed  time.Duration
	Polls    int
	MaxGap   time.Duration
	ErrCount int
	FirstErr error
	LastErr  error
}

// Error returns the observation and the cause it supports. The three branches
// are mutually exclusive and ordered by which finding supersedes which: a shard
// that could not be asked at all makes the timing moot, and a starved poller
// makes a "did not elect" verdict unsupportable.
func (e *ShardLeaderWaitError) Error() string {
	var b strings.Builder
	b.WriteString("server: shard ")
	b.WriteString(strconv.FormatUint(e.ShardID, 10))
	b.WriteString(": no raft leader after ")
	b.WriteString(e.Elapsed.Round(time.Millisecond).String())
	b.WriteString(" (budget ")
	b.WriteString(e.Timeout.String())
	b.WriteString(", ")
	b.WriteString(strconv.Itoa(e.Polls))
	b.WriteString(" polls, longest gap ")
	b.WriteString(e.MaxGap.Round(time.Millisecond).String())
	b.WriteString("): ")

	switch {
	case e.LastErr != nil:
		// The timeout is a symptom; this is the finding. The COUNT is what
		// makes it actionable: a shard that could not be asked on every poll is
		// a different problem from one that failed on three of forty-seven, and
		// the second is invisible to a fixture that errors uniformly.
		b.WriteString("the shard COULD NOT BE ASKED on ")
		b.WriteString(strconv.Itoa(e.ErrCount))
		b.WriteString(" of ")
		b.WriteString(strconv.Itoa(e.Polls))
		b.WriteString(" polls (")
		if e.ErrCount == e.Polls {
			b.WriteString("persistent")
		} else {
			b.WriteString("INTERMITTENT -- the other polls answered cleanly with no leader")
		}
		b.WriteString(") -- GetLeaderID last failed with: ")
		b.WriteString(e.LastErr.Error())
		if e.FirstErr != nil && e.FirstErr.Error() != e.LastErr.Error() {
			b.WriteString("; first failure differed: ")
			b.WriteString(e.FirstErr.Error())
		}
		b.WriteString(". Fix that before reading anything into the timing")
	case e.Starved():
		b.WriteString("the POLLER WAS STARVED -- ")
		b.WriteString(e.MaxGap.Round(time.Millisecond).String())
		b.WriteString(" elapsed between two consecutive polls against a ")
		b.WriteString(e.Interval.String())
		b.WriteString(" interval, so the scheduler, not the shard, consumed the budget. ")
		b.WriteString("This is host contention (go-qrdb#139), not an election defect. ")
		b.WriteString("Re-run on an idle host before suspecting the code, and do NOT widen the budget")
	default:
		// The honest red. The poller kept asking, on time, for the whole
		// budget, and the shard never elected.
		b.WriteString("the shard GENUINELY DID NOT ELECT -- the poller ran on time for the whole budget ")
		b.WriteString("(longest gap ")
		b.WriteString(e.MaxGap.Round(time.Millisecond).String())
		b.WriteString(", within tolerance), so this is NOT host contention. ")
		b.WriteString("This is a real election failure; investigate the shard, not the runner")
	}
	return b.String()
}

// Starved reports whether the gap between two consecutive polls exceeded
// shardLeaderStarvationFactor poll intervals, which is the discriminator
// between a busy host and a broken election.
//
// It is a method rather than a field so the threshold has exactly one
// definition and a caller cannot record a verdict that disagrees with the
// measurement it was derived from.
func (e *ShardLeaderWaitError) Starved() bool {
	return e.Interval > 0 && e.MaxGap >= shardLeaderStarvationFactor*e.Interval
}

// Unwrap returns ErrTenantShardNoLeader so errors.Is(err, ErrTenantShardNoLeader)
// continues to hold for every existing caller.
func (e *ShardLeaderWaitError) Unwrap() error { return ErrTenantShardNoLeader }

// TenantHostError wraps a failure building, resolving, or closing a per-tenant
// volume Raft Host. It identifies the tenant and the operation that failed.
type TenantHostError struct {
	TenantID string
	Op       string
	Err      error
}

// Error implements the error interface.
func (e *TenantHostError) Error() string {
	return "server: tenant host " + e.Op + " for tenant " + e.TenantID + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for errors.Is/As.
func (e *TenantHostError) Unwrap() error { return e.Err }

// VolumeReattachError is returned when an IsolationVolume tenant cannot be
// re-bound to its on-disk state during boot (barrier re-registration, unseal,
// or per-tenant Host construction failed).
type VolumeReattachError struct {
	TenantID string
	Err      error
}

// Error implements the error interface.
func (e *VolumeReattachError) Error() string {
	return "server: re-attach volume tenant " + e.TenantID + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for errors.Is/As.
func (e *VolumeReattachError) Unwrap() error { return e.Err }

// RaftReattachError is returned when an IsolationRaft tenant cannot be re-bound
// to its on-disk state during boot (barrier re-registration, unseal, or a
// dedicated database shard re-start failed).
type RaftReattachError struct {
	TenantID string
	Database string
	Err      error
}

// Error implements the error interface.
func (e *RaftReattachError) Error() string {
	if e.Database != "" {
		return "server: re-attach raft tenant " + e.TenantID + "/" + e.Database + ": " + e.Err.Error()
	}
	return "server: re-attach raft tenant " + e.TenantID + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for errors.Is/As.
func (e *RaftReattachError) Unwrap() error { return e.Err }

// TenantInfraConfigError is returned by BuildTenantInfra when the caller
// supplies an incompatible combination of TenantInfraConfig fields. It is a
// programmer / configuration error — the node must not proceed.
//
// The canonical case is Host != nil && Manager == nil: IsolationRaft requires
// a database.Manager to track per-tenant shard metadata and to walk existing
// shards during reattach. Without one the factory would panic inside
// NewRaftBackendFactoryFromManager or reattachRaftTenants.
type TenantInfraConfigError struct {
	// Field is the name of the misconfigured TenantInfraConfig field.
	Field string
	// Message describes the constraint that was violated.
	Message string
}

// Error implements the error interface.
func (e *TenantInfraConfigError) Error() string {
	return "server: tenant infra config: " + e.Field + ": " + e.Message
}
