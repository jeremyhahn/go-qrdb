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

package kvstore

import "reflect"

// UsableAsMapKey reports whether a store may be used as a map or sync.Map
// key.
//
// Routing layers cache one per-tenant service per RESOLVED store, keyed by
// the store itself, because store identity is what distinguishes one tenant's
// backing from another's. That is a sound cache key only for a store whose
// dynamic type is comparable — and KVStore is an interface, so nothing
// requires that. A named slice or map type with value receivers satisfies the
// interface perfectly well, and using such a value as a key panics with
// "hash of unhashable type" on the FIRST operation, before any tenant work
// runs.
//
// A store that cannot be a key is not an error and must not be refused: the
// caller's choice of representation is its own. The cache is an optimization,
// so a non-comparable store simply skips it and gets a freshly built service.
//
// This lives here, beside the interface it interrogates, because three
// routing layers need the identical judgement and two of them did not make
// it: pkg/invite guarded its cache and pkg/user and pkg/role — the very
// packages invite's cache was modelled on — did not, so a contract-compliant
// resolver crashed them (go-qrdb#41). One helper, one answer.
func UsableAsMapKey(s KVStore) bool {
	return s != nil && reflect.TypeOf(s).Comparable()
}
