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

// Package capabilities provides the runtime feature truth of a QRDB
// process. Build-tagged features register themselves via init(); at boot
// the composition root seals the effective profile — the intersection of
// build tags, run mode, and configuration — and every transport reports
// it through the single service funnel. A false value can therefore mean
// not-compiled, mode-gated, or config-disabled.
package capabilities

import (
	"slices"
	"sync"
)

// Run modes reported through Capabilities.Mode.
const (
	// ModeStandalone is the raft-free run mode: standard database
	// operations plus the always-on security core.
	ModeStandalone = "standalone"

	// ModeRaft is the consensus run mode (single- or multi-node): the
	// full feature set including MVCC, watch, lease, lock, transactions,
	// etcd compatibility, and cluster operations.
	ModeRaft = "raft"
)

// RaftOnlyHint is the remedy text carried by every refusal of a raft-only
// service family (MVCC, watch, lease, lock) under mode "standalone", on every
// transport including the embedded one.
const RaftOnlyHint = "requires mode: raft"

// Capabilities describes the effective feature set of the QRDB process:
// what is compiled in (build tags), structurally possible (run mode), and
// operator-enabled (config). Consumers must treat every field as
// authoritative — a false value may mean the feature is not compiled, is
// forbidden by the run mode, or was disabled in configuration.
type Capabilities struct {
	Mode        string `json:"mode"`
	KV          bool   `json:"kv"`
	MVCC        bool   `json:"mvcc"`
	Transaction bool   `json:"transaction"`
	Batch       bool   `json:"batch"`
	Watch       bool   `json:"watch"`
	Lease       bool   `json:"lease"`
	Lock        bool   `json:"lock"`
	Index       bool   `json:"index"`
	Cluster     bool   `json:"cluster"`
	Backup      bool   `json:"backup"`
	Rebalance   bool   `json:"rebalance"`
	TimeSeries  bool   `json:"timeSeries"`
	Vector      bool   `json:"vector"`
	SQL         bool   `json:"sql"`
	MySQL       bool   `json:"mysql"`
	PostgreSQL  bool   `json:"postgresql"`
	Etcd        bool   `json:"etcd"`
	FTS         bool   `json:"fts"`

	// UnencryptedAtRest lists, when seal is enabled, where this node keeps
	// data on disk outside the barrier's at-rest encryption -- a state
	// machine or a local storage backend that does not route its files
	// through the barrier. Empty when seal is off or everything on disk is
	// encrypted. It is a report, not a refusal: the node runs, and says so.
	UnencryptedAtRest []string `json:"unencryptedAtRest,omitempty"`
}

var (
	mu       sync.RWMutex
	features = map[string]struct{}{}
	sealed   *Capabilities
)

// Register records a feature as compiled in. It is called from init()
// functions in build-tagged files.
func Register(feature string) {
	mu.Lock()
	features[feature] = struct{}{}
	mu.Unlock()
}

// Seal stores the effective capability profile computed by the
// composition root at boot (build tags ∩ run mode ∩ config). After Seal,
// Get returns exactly this profile. Sealing again replaces the profile;
// composition roots must seal exactly once, before serving traffic.
func Seal(c Capabilities) {
	c.UnencryptedAtRest = slices.Clone(c.UnencryptedAtRest)
	mu.Lock()
	sealed = &c
	mu.Unlock()
}

// Sealed reports whether a composition root has sealed the effective
// profile for this process.
func Sealed() bool {
	mu.RLock()
	defer mu.RUnlock()
	return sealed != nil
}

// Get returns the effective capability profile. When a composition root
// has sealed one, that profile is returned verbatim. Before sealing —
// library embeddings and tests that never boot a composition root — it
// falls back to the historical compile-time view: raft mode with every
// core feature enabled and build-tagged features reflecting registration.
func Get() Capabilities {
	mu.RLock()
	defer mu.RUnlock()

	if sealed != nil {
		c := *sealed
		c.UnencryptedAtRest = slices.Clone(c.UnencryptedAtRest)
		return c
	}

	_, ts := features["timeSeries"]
	_, vec := features["vector"]
	_, sql := features["sql"]
	_, mysql := features["mysql"]
	_, pgsql := features["pgsql"]
	_, etcd := features["etcd"]
	_, fts := features["fts"]

	return Capabilities{
		Mode:        ModeRaft,
		KV:          true,
		MVCC:        true,
		Transaction: true,
		Batch:       true,
		Watch:       true,
		Lease:       true,
		Lock:        true,
		Index:       true,
		Cluster:     true,
		Backup:      true,
		Rebalance:   true,
		TimeSeries:  ts,
		Vector:      vec,
		SQL:         sql,
		MySQL:       mysql,
		PostgreSQL:  pgsql,
		Etcd:        etcd,
		FTS:         fts,
	}
}

// Has reports whether the named feature has been registered by a
// build-tagged init(). It answers "compiled in?", not "effective?" — use
// Get for the sealed runtime truth.
func Has(feature string) bool {
	mu.RLock()
	_, ok := features[feature]
	mu.RUnlock()
	return ok
}

// Reset clears all registered features and any sealed profile. This is
// intended for testing only and must not be called from production code.
func Reset() {
	mu.Lock()
	features = map[string]struct{}{}
	sealed = nil
	mu.Unlock()
}
