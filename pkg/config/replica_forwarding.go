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

import "time"

// ReplicaForwardingConfig configures replica forwarding: a raft-mode node
// that holds no replica of a database forwards a request for it to a node
// that does, as the caller (docs/replica-forwarding).
type ReplicaForwardingConfig struct {
	// Enabled turns forwarding on. Nil means on in raft mode; a node with
	// it off answers every request for a database it holds no replica of
	// with the typed refusal and the replica set.
	Enabled *bool `yaml:"enabled"`

	// Timeout bounds one forward, connection included, as a Go duration
	// string. Default: "10s".
	Timeout string `yaml:"timeout"`
}

// DefaultReplicaForwardingTimeout is replica_forwarding.timeout's default.
const DefaultReplicaForwardingTimeout = 10 * time.Second

// On reports whether forwarding is on: unset means on.
func (c ReplicaForwardingConfig) On() bool {
	return c.Enabled == nil || *c.Enabled
}

// TimeoutDuration returns replica_forwarding.timeout as a duration:
// DefaultReplicaForwardingTimeout when unset.
func (c ReplicaForwardingConfig) TimeoutDuration() (time.Duration, error) {
	if c.Timeout == "" {
		return DefaultReplicaForwardingTimeout, nil
	}
	d, err := parseDuration(c.Timeout)
	if err != nil || d <= 0 {
		return 0, &ValidationError{Field: "replica_forwarding.timeout", Reason: "must be a valid Go duration string greater than zero"}
	}
	return d, nil
}
