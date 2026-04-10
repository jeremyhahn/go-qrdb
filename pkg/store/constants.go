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

package store

import "strings"

// metaKeyLastApplied is the reserved key used to persist the last applied
// Raft log index for crash recovery. Uses a null prefix to sort before user data.
const metaKeyLastApplied = "\x00__quicraft_last_applied__"

// internalPrefixes lists key prefixes used by internal subsystems (e.g.
// roles, users, indexes) that should be excluded from user-facing key lists.
var internalPrefixes = []string{"__sys:", "ts:", "idx:metric:", "idx:tag:", "retention:"}

// isInternalKey reports whether k starts with any internal prefix.
func isInternalKey(k string) bool {
	for _, p := range internalPrefixes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}
