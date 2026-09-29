// Copyright 2026 Jeremy Hahn
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

package keydelivery

import quicraftseal "github.com/jeremyhahn/go-quicraft/pkg/seal"

// fipsMode is the stack's single FIPS 140 mode predicate (re-exported by
// pkg/seal as FIPSEnabled). It is a variable so a test can assert the FIPS
// ruling in a standard runtime and the standard ruling in a FIPS one.
var fipsMode = quicraftseal.FIPSEnabled

// SuiteNotApprovedError is returned when a suite that is not FIPS 140-3
// approved is asked for in FIPS mode. X25519 is not an approved key
// agreement, so DHKEM(X25519) and the ML-KEM-768 + X25519 hybrid are both
// refused: a deployment that has asked for FIPS mode is never handed an
// envelope built on them, even where the runtime would tolerate it.
type SuiteNotApprovedError struct {
	// Name is the refused KEM name.
	Name string
}

// Error returns a human-readable description.
func (e *SuiteNotApprovedError) Error() string {
	return "keydelivery: KEM " + `"` + e.Name + `"` + " is not FIPS 140-3 approved"
}
