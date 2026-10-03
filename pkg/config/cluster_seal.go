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
	"strings"
	"time"
)

// Defaults for the cluster seal timing (docs/barrier-seal/engineering.md).
const (
	DefaultClusterLeaseString        = "15s"
	DefaultClusterPollIntervalString = "1s"
	DefaultClusterAckTimeoutString   = "5s"
)

// MaxClusterLease caps seal.cluster_lease: a node cut off from the seal
// records keeps serving until its lease runs out, so the lease is the longest
// a seal can go unenforced on such a node.
const MaxClusterLease = 60 * time.Second

// ClusterSealTiming is the cluster seal monitor's timing.
type ClusterSealTiming struct {
	// Lease is how long a successful read of the seal records lets the node
	// serve.
	Lease time.Duration
	// PollInterval is how often the node reads the records.
	PollInterval time.Duration
	// AckTimeout bounds the wait for every node's acknowledgement before a
	// node key is wiped.
	AckTimeout time.Duration
}

// ClusterSealTiming parses seal.cluster_lease, seal.cluster_poll_interval and
// seal.cluster_ack_timeout, applying their defaults. Every value must be a
// positive duration and the poll interval at most a third of the lease, so a
// node renews its lease well before it runs out; anything else is a
// *ValidationError naming the field.
func (c SealConfig) ClusterSealTiming() (ClusterSealTiming, error) {
	lease, err := positiveDuration("seal.cluster_lease", c.ClusterLease, DefaultClusterLeaseString)
	if err != nil {
		return ClusterSealTiming{}, err
	}
	poll, err := positiveDuration("seal.cluster_poll_interval", c.ClusterPollInterval, DefaultClusterPollIntervalString)
	if err != nil {
		return ClusterSealTiming{}, err
	}
	ack, err := positiveDuration("seal.cluster_ack_timeout", c.ClusterAckTimeout, DefaultClusterAckTimeoutString)
	if err != nil {
		return ClusterSealTiming{}, err
	}
	if lease > MaxClusterLease {
		return ClusterSealTiming{}, &ValidationError{Field: "seal.cluster_lease", Reason: "must be at most " + MaxClusterLease.String() + ": a cut-off node serves until its lease runs out"}
	}
	if 3*poll > lease {
		return ClusterSealTiming{}, &ValidationError{Field: "seal.cluster_poll_interval", Reason: "must be at most a third of seal.cluster_lease"}
	}
	if 2*ack > lease {
		return ClusterSealTiming{}, &ValidationError{Field: "seal.cluster_ack_timeout", Reason: "must be at most half of seal.cluster_lease"}
	}
	return ClusterSealTiming{Lease: lease, PollInterval: poll, AckTimeout: ack}, nil
}

// positiveDuration parses value, or def when value is blank, and refuses
// anything that is not a positive duration.
func positiveDuration(field, value, def string) (time.Duration, error) {
	s := strings.TrimSpace(value)
	if s == "" {
		s = def
	}
	d, err := parseDuration(s)
	if err != nil {
		return 0, &ValidationError{Field: field, Reason: "must be a valid Go duration (e.g. " + def + ")"}
	}
	if d <= 0 {
		return 0, &ValidationError{Field: field, Reason: "must be greater than zero"}
	}
	return d, nil
}
