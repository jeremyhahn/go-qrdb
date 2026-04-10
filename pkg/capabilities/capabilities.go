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

// Package capabilities provides compile-time feature registration that
// flows from build tags through the service layer to all API protocols
// and SDKs. Optional features register themselves via init() in
// build-tagged files.
package capabilities

import "sync"

// Capabilities describes the compile-time feature set of the QRDB binary.
// Core features (KV, MVCC, Transaction, Batch, Watch, Lease, Index) are
// always enabled. Optional features (TimeSeries, Vector) are gated by
// build tags.
type Capabilities struct {
	KV          bool `json:"kv"`
	MVCC        bool `json:"mvcc"`
	Transaction bool `json:"transaction"`
	Batch       bool `json:"batch"`
	Watch       bool `json:"watch"`
	Lease       bool `json:"lease"`
	Index       bool `json:"index"`
	TimeSeries  bool `json:"timeSeries"`
	Vector      bool `json:"vector"`
	MySQL       bool `json:"mysql"`
	PostgreSQL  bool `json:"postgresql"`
}

var (
	mu       sync.RWMutex
	features = map[string]struct{}{}
)

// Register records a feature as available. It is called from init()
// functions in build-tagged files.
func Register(feature string) {
	mu.Lock()
	features[feature] = struct{}{}
	mu.Unlock()
}

// Get returns the current set of registered capabilities. Core
// features are always true; optional features reflect whether their
// build-tagged init() was linked into the binary.
func Get() Capabilities {
	mu.RLock()
	defer mu.RUnlock()

	_, ts := features["timeSeries"]
	_, vec := features["vector"]
	_, mysql := features["mysql"]
	_, pgsql := features["pgsql"]

	return Capabilities{
		KV:          true,
		MVCC:        true,
		Transaction: true,
		Batch:       true,
		Watch:       true,
		Lease:       true,
		Index:       true,
		TimeSeries:  ts,
		Vector:      vec,
		MySQL:       mysql,
		PostgreSQL:  pgsql,
	}
}

// Has reports whether the named feature has been registered.
func Has(feature string) bool {
	mu.RLock()
	_, ok := features[feature]
	mu.RUnlock()
	return ok
}

// Reset clears all registered features. This is intended for testing
// only and must not be called from production code.
func Reset() {
	mu.Lock()
	features = map[string]struct{}{}
	mu.Unlock()
}
