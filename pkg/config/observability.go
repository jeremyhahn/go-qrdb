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

package config

import (
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/syncwatch"
)

// ObservabilityConfig configures the signals a node emits about itself
// (docs/api-server).
type ObservabilityConfig struct {
	// SlowSyncThreshold is the duration above which one durable sync -- a
	// Raft WAL fsync or a storage backend's sync -- logs a slow-sync
	// warning, as a Go duration string. It is an observation threshold for
	// the stall signal only, not a durability knob: every sync still
	// happens. Default: "1s"; it must be greater than zero and at most
	// one minute.
	SlowSyncThreshold string `yaml:"slow_sync_threshold"`
}

// DefaultSlowSyncThreshold is observability.slow_sync_threshold's default:
// etcd's slow-fdatasync threshold.
const DefaultSlowSyncThreshold = syncwatch.DefaultSlowThreshold

// MaxSlowSyncThreshold is the largest observability.slow_sync_threshold
// accepted. A sync stalled that long has long since cost a Raft leader its
// lease, so a higher threshold could only hide the stall.
const MaxSlowSyncThreshold = time.Minute

// SlowSyncThresholdDuration returns observability.slow_sync_threshold as a
// duration: DefaultSlowSyncThreshold when unset, and a ValidationError when
// it is malformed, not greater than zero, or above MaxSlowSyncThreshold.
func (c ObservabilityConfig) SlowSyncThresholdDuration() (time.Duration, error) {
	if c.SlowSyncThreshold == "" {
		return DefaultSlowSyncThreshold, nil
	}
	d, err := parseDuration(c.SlowSyncThreshold)
	if err != nil || d <= 0 || d > MaxSlowSyncThreshold {
		return 0, &ValidationError{Field: "observability.slow_sync_threshold", Reason: "must be a valid Go duration string greater than zero and at most 1m"}
	}
	return d, nil
}

// ApplySlowSyncThreshold validates observability.slow_sync_threshold and
// makes it the process's slow-sync threshold (syncwatch.SetSlowThreshold),
// which the observer of the Raft WAL and of every storage backend applies.
// Both boot paths -- qrdbd start and the Go SDK's node constructors -- call
// it before they open any storage. A refused value leaves the process's
// threshold as it was.
func (c ObservabilityConfig) ApplySlowSyncThreshold() error {
	d, err := c.SlowSyncThresholdDuration()
	if err != nil {
		return err
	}
	syncwatch.SetSlowThreshold(d)
	return nil
}
