// Copyright (c) 2026 Jeremy Hahn
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

// Package publicpath holds the built-in pre-authentication allowlist: the
// exact request paths the REST authentication middleware exempts from
// authentication whatever the request method. A handler mounted on such a
// path never sees a caller identity, so a registration that needs one must
// refuse it.
package publicpath

// paths is the built-in pre-authentication allowlist. See the comment at its
// use in the REST authentication middleware for why each path is public.
var paths = map[string]struct{}{
	"/v1/ca/bootstrap":        {},
	"/v1/config/initialized":  {},
	"/v1/cluster/keydelivery": {},
	"/v1/cluster/nodekey":     {},
}

// Is reports whether path is on the built-in pre-authentication allowlist.
// Only an exact match counts.
func Is(path string) bool {
	_, ok := paths[path]
	return ok
}
