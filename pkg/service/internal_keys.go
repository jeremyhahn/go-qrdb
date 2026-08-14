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

package service

import "strings"

// ---------------------------------------------------------------------------
// Internal key namespace
//
// A handful of key prefixes belong to internal subsystems rather than to the
// user keyspace: RBAC roles and users, time-series points and their indexes,
// and retention policies. They are stored in the same MVCC keyspace as user
// data, so every user-facing surface that enumerates or mutates keys in bulk
// must exclude them.
//
// This is the ONE canonical declaration of that set. pkg/engine and pkg/store
// delegate to it so a prefix added here is honoured by every layer at once —
// a subsystem that registered a new internal prefix in only one of three
// copies would silently expose its records through the other two.
// ---------------------------------------------------------------------------

// InternalKeyPrefixSystem is the prefix reserved for the internal system
// stores (roles, users, and other RBAC records). Restoring or enumerating
// keys under it through a user-facing surface is a privilege-escalation
// vector and is refused.
const InternalKeyPrefixSystem = "__sys:"

// internalKeyPrefixes is the canonical set of key prefixes reserved for
// internal subsystems. Order is not significant.
var internalKeyPrefixes = []string{
	InternalKeyPrefixSystem, // RBAC roles, users, and other system records
	"ts:",                   // time-series points
	"idx:metric:",           // time-series metric index
	"idx:tag:",              // time-series tag index
	"retention:",            // time-series retention policies
}

// InternalKeyPrefixes returns a copy of the canonical internal key prefixes.
// A copy is returned so a caller cannot mutate the guard's own backing array
// and quietly widen the user keyspace.
func InternalKeyPrefixes() []string {
	out := make([]string, len(internalKeyPrefixes))
	copy(out, internalKeyPrefixes)
	return out
}

// IsInternalKey reports whether key lies in the internal key namespace, i.e.
// whether it begins with any of the reserved internal prefixes. It is the
// single predicate every layer uses to decide that a key is not the caller's
// to read in bulk or to write.
func IsInternalKey(key string) bool {
	for _, p := range internalKeyPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}
