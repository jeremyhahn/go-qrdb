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

package index

import "bytes"

// CatalogPrefix is the raw state-machine key prefix the secondary-index
// catalog is stored under. Each record is keyed by the index name and holds
// the index-create command payload exactly as protocol.DecodeIndexCreateOpts
// reads it, so the definition an engine restores on open is the one the
// create applied, with no second encoding to drift from it.
//
// The catalog is replicated state like the lease table: the create and drop
// write it in the apply batch, snapshots carry it and replica hashes cover
// it, and the leading 0x00 keeps it out of user key enumeration.
const CatalogPrefix = "\x00qrdb.index/"

// IsCatalogKey reports whether k is an index-catalog record.
func IsCatalogKey(k []byte) bool {
	return bytes.HasPrefix(k, []byte(CatalogPrefix))
}

// CatalogKey returns the catalog key of the index named name.
func CatalogKey(name string) []byte {
	return []byte(CatalogPrefix + name)
}

// CatalogRange returns the half-open key range [start, end) that holds every
// catalog record.
func CatalogRange() (start, end []byte) {
	start = []byte(CatalogPrefix)
	end = []byte(CatalogPrefix)
	end[len(end)-1]++
	return start, end
}
