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

package ledger

import "strconv"

// AmbiguousValueError is a history that writes one value to one key from two
// operations, so a read of that value cannot be attributed to either.
type AmbiguousValueError struct {
	Key   string
	Value string
	OpIDs []uint64
}

func (e *AmbiguousValueError) Error() string {
	s := "ledger: key " + strconv.Quote(e.Key) + ": value " + strconv.Quote(e.Value) + " written by more than one op:"
	for _, id := range e.OpIDs {
		s += " " + strconv.FormatUint(id, 10)
	}
	return s
}
