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

package linearizability

import (
	"strconv"
	"time"
)

// TimeoutError is a key whose operations the search could not decide within
// the checker's timeout: neither a linear order nor proof that none exists.
type TimeoutError struct {
	Key     string
	Ops     int
	Timeout time.Duration
}

func (e *TimeoutError) Error() string {
	return "linearizability: key " + strconv.Quote(e.Key) + ": " + strconv.Itoa(e.Ops) +
		" operations undecided after " + e.Timeout.String()
}

// AmbiguousCASError is a compare-and-swap with no expected value. The history
// format cannot tell an empty comparand from an absent one, and the two mean
// different things to a store, so the checker refuses rather than guess.
type AmbiguousCASError struct {
	ID uint64
}

func (e *AmbiguousCASError) Error() string {
	return "linearizability: op " + strconv.FormatUint(e.ID, 10) + ": cas without an expected value"
}
