//go:build qrdb_sql

// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pgcatalog

import (
	"hash/fnv"
	"math"
	"strings"
)

// firstUserOID is PostgreSQL's FirstNormalObjectId: OIDs below it are
// reserved for the objects a PostgreSQL installation is bootstrapped with,
// which for this package means the pg_type rows in pgTypes. Every derived
// OID is placed at or above it so a derived value can never collide with a
// hard-coded type OID.
const firstUserOID = 16384

// oidSpan is the number of distinct values a derived OID can take.
//
// The ceiling is MaxInt32 rather than PostgreSQL's own MaxUint32 so that
// every OID this package produces is representable in the signed 32-bit
// integer the PostgreSQL wire layer describes an oid column as (int4 --
// PostgreSQL's oid type is unsigned, but its wire representation is not,
// and pkg/sql/pgsql maps a 32-bit column to Int4OID). Giving up the top bit
// costs nothing: 2^31 values against the dozens of relations a schema holds
// leaves the collision probability far below any other failure mode here.
const oidSpan = uint64(math.MaxInt32) - firstUserOID + 1

// namespaceOIDKind and classOIDKind are the domain separators mixed into a
// derived OID. Without them NamespaceOID("gorm") and ClassOID("gorm") would
// hash identical input and a schema could collide with a relation of the
// same name -- the exact case a client hits when a database and one of its
// tables share a name.
const (
	namespaceOIDKind = "pg_namespace"
	classOIDKind     = "pg_class"
)

// derivedOID hashes parts into the user-OID range.
//
// It must be a pure function of the qualified name and nothing else. A
// PostgreSQL client may be served by ANY replica -- and by different
// replicas for two statements of the same logical operation, since
// gorm.io/driver/postgres reads pg_class.oid in one subquery and joins it
// against pg_description.objoid in the same statement but reads
// information_schema on its own -- so an OID derived from anything
// node-local (a counter, an address, a start time) would differ between
// replicas and make the join fail nondeterministically. FNV-1a over the
// lowercased name is stable across processes, restarts, replicas and
// architectures.
//
// Parts are separated by a NUL byte, which cannot occur in a SQL
// identifier, so ("ab", "c") and ("a", "bc") hash differently.
func derivedOID(kind string, parts ...string) uint32 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(kind))
	for _, p := range parts {
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(strings.ToLower(p)))
	}
	return firstUserOID + uint32(h.Sum64()%oidSpan)
}

// NamespaceOID returns the pg_catalog.pg_namespace.oid reported for the
// schema named nspname.
func NamespaceOID(nspname string) uint32 {
	return derivedOID(namespaceOIDKind, nspname)
}

// ClassOID returns the pg_catalog.pg_class.oid reported for the relation
// named relname in the schema named nspname. Both parts participate so two
// same-named tables in different schemas get different OIDs.
func ClassOID(nspname, relname string) uint32 {
	return derivedOID(classOIDKind, nspname, relname)
}
