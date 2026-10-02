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

package tenant

import "bytes"

// NamespaceOf returns the "<tenant>/<db>/" namespace prefix key lies under,
// and false when key cannot lie under any (tenant, database) namespace.
//
// It is structural, deliberately: a key has a namespace exactly when it has
// the shape ComposeNamespace produces -- a non-empty segment, '/', a
// non-empty segment, '/' -- whatever the segments say. It does not ask
// whether the tenant exists or whether its id would pass validation, so no
// tenant's database, known to this node or not, can fall outside it. A key
// that merely looks namespaced (an internal record such as
// "__sys:cluster/features/x") is reported as namespaced too; for the
// node-keyspace compaction that relies on this, that errs toward keeping
// history, never toward discarding a tenant's.
func NamespaceOf(key []byte) ([]byte, bool) {
	first := bytes.IndexByte(key, '/')
	if first <= 0 {
		return nil, false
	}
	second := bytes.IndexByte(key[first+1:], '/')
	if second <= 0 {
		return nil, false
	}
	return key[:first+1+second+1], true
}
