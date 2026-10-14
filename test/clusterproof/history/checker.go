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

//go:build clusterproof

package history

// Checker judges a history. The acknowledged-write ledger is one; a
// linearizability checker plugs in beside it through the same interface.
//
// Check returns an error only when it could not judge -- a history it cannot
// interpret -- and never for a history it judged and found wanting: that is a
// Result with Valid false and the anomalies that make it so.
type Checker interface {
	Check(h History) (Result, error)
}

// Result is one checker's verdict on one history.
type Result struct {
	// Checker names the checker that produced the verdict.
	Checker string `json:"checker"`
	// Valid is true only when the checker examined the history and found
	// no anomaly. A checker that had nothing to examine is not valid.
	Valid bool `json:"valid"`
	// Checked is how many operations the verdict rests on, in the
	// checker's own unit, so an evidence bundle shows what was proven.
	Checked int `json:"checked"`
	// Anomalies are the findings, each naming the operations behind it.
	Anomalies []Anomaly `json:"anomalies,omitempty"`
}

// Anomaly is one finding. Kind is the checker's own vocabulary; OpIDs are the
// operations a reader must look at to see it.
type Anomaly struct {
	Kind   string   `json:"kind"`
	Key    string   `json:"key,omitempty"`
	Value  string   `json:"value,omitempty"`
	OpIDs  []uint64 `json:"op_ids,omitempty"`
	Detail string   `json:"detail"`
}
