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

// Package vector holds the record formats, key layout, and index math
// (bloom, flat, gpu, hnsw, ivf, kmeans, quantization, scalar) behind qrdb's
// vector namespaces.
//
// Vectors live in the database's own store, inside the tenant boundary, as
// an engine of the database binding like KV and time series: see
// docs/vectors/architecture.md and docs/vectors/engineering.md ("Target:
// the vector engine"). This file and record.go define the keys and the
// values pkg/engine's apply handlers read and write; they hold no apply
// logic of their own, so they have no dependency on pkg/engine, storage, or
// Raft.
package vector

import "encoding/hex"

// KeyPrefix is the reserved key prefix every vector-engine key carries. It is
// covered by the internal-key guard (service.IsInternalKey) so vector keys
// never appear in a database's ordinary KV enumeration, exactly as "__ts:"
// and "__sys:" do not.
const KeyPrefix = "__vec:"

// Key segment prefixes, each following KeyPrefix.
const (
	namespaceSegment    = "ns/"
	vectorSegment       = "v/"
	metadataSegment     = "md/"
	mutationLogSegment  = "ml/"
	vectorIDHexLen      = 16 // 8 bytes, hex-encoded (see idHex)
	mutationLogOpUpsert = byte(0)
	mutationLogOpDelete = byte(1)
	mutationLogOpUpdate = byte(2)
)

// MutationOp names the kind of mutation a mutation-log entry records.
type MutationOp = byte

// The mutation kinds a mutation-log entry can record.
const (
	MutationUpsert MutationOp = mutationLogOpUpsert
	MutationDelete MutationOp = mutationLogOpDelete
	MutationUpdate MutationOp = mutationLogOpUpdate
)

// NamespaceKey returns the key of a namespace's record: "__vec:ns/<ns>".
func NamespaceKey(namespace string) []byte {
	return append([]byte(KeyPrefix+namespaceSegment), namespace...)
}

// NamespacePrefix returns the common prefix of every namespace record, for
// listing every namespace in a database's store.
func NamespacePrefix() []byte {
	return []byte(KeyPrefix + namespaceSegment)
}

// VectorKey returns the key of one vector record: "__vec:v/<ns>/<id: 16 hex
// digits, big-endian>". See idHex for why IDs are hex, not raw bytes.
func VectorKey(namespace string, id uint64) []byte {
	key := make([]byte, 0, len(KeyPrefix)+len(vectorSegment)+len(namespace)+1+vectorIDHexLen)
	key = append(key, KeyPrefix+vectorSegment...)
	key = append(key, namespace...)
	key = append(key, '/')
	return appendIDHex(key, id)
}

// VectorPrefix returns the common prefix of every vector record in namespace,
// for a namespace-wide scan (rebuild, delete-namespace, export).
func VectorPrefix(namespace string) []byte {
	return append([]byte(KeyPrefix+vectorSegment+namespace), '/')
}

// MetadataKey returns the key of one metadata-index entry:
// "__vec:md/<ns>/<field, hex>/<value, hex>/<id: 16 hex digits, big-endian>".
// See hexSegment for why the field and value are hex, not raw bytes.
func MetadataKey(namespace, field, value string, id uint64) []byte {
	fieldHex, valueHex := hexSegment(field), hexSegment(value)
	key := make([]byte, 0, len(KeyPrefix)+len(metadataSegment)+len(namespace)+1+
		len(fieldHex)+1+len(valueHex)+1+vectorIDHexLen)
	key = append(key, KeyPrefix+metadataSegment...)
	key = append(key, namespace...)
	key = append(key, '/')
	key = append(key, fieldHex...)
	key = append(key, '/')
	key = append(key, valueHex...)
	key = append(key, '/')
	return appendIDHex(key, id)
}

// MetadataFieldValuePrefix returns the common prefix of every metadata-index
// entry for one (field, value) pair, for a metadata-equality lookup.
func MetadataFieldValuePrefix(namespace, field, value string) []byte {
	fieldHex, valueHex := hexSegment(field), hexSegment(value)
	key := make([]byte, 0, len(KeyPrefix)+len(metadataSegment)+len(namespace)+1+
		len(fieldHex)+1+len(valueHex)+1)
	key = append(key, KeyPrefix+metadataSegment...)
	key = append(key, namespace...)
	key = append(key, '/')
	key = append(key, fieldHex...)
	key = append(key, '/')
	key = append(key, valueHex...)
	key = append(key, '/')
	return key
}

// MetadataPrefix returns the common prefix of every metadata-index entry in
// namespace, for a namespace-wide scan (delete-namespace).
func MetadataPrefix(namespace string) []byte {
	return append([]byte(KeyPrefix+metadataSegment+namespace), '/')
}

// MutationLogKey returns the key of one mutation-log entry:
// "__vec:ml/<ns>/<seq: 16 hex digits, big-endian>".
func MutationLogKey(namespace string, seq uint64) []byte {
	key := make([]byte, 0, len(KeyPrefix)+len(mutationLogSegment)+len(namespace)+1+vectorIDHexLen)
	key = append(key, KeyPrefix+mutationLogSegment...)
	key = append(key, namespace...)
	key = append(key, '/')
	return appendIDHex(key, seq)
}

// MutationLogPrefix returns the common prefix of every mutation-log entry in
// namespace, for replay and for a namespace-wide scan (delete-namespace).
func MutationLogPrefix(namespace string) []byte {
	return append([]byte(KeyPrefix+mutationLogSegment+namespace), '/')
}

// appendIDHex appends v to buf as 16 lowercase hex digits of its big-endian
// byte representation, so a lexicographic key scan visits vector IDs and
// mutation-log sequences in numeric order -- fixed-width hex of a
// fixed-width big-endian integer preserves the same ordering raw big-endian
// bytes would, digit for digit.
//
// Hex, not raw bytes: a raw big-endian uint64 is mostly 0x00 bytes for every
// realistic ID (the common case -- small, sequentially-allocated IDs -- is
// the WORST case, since every leading byte up to the value itself is zero),
// and go-objstore's local provider refuses a key containing a NUL byte. An
// engine key embedding a raw binary integer therefore cannot be written
// through that backend at all; hex-encoding removes the NUL byte from the
// key while keeping the ordering property range scans and replay depend on.
func appendIDHex(buf []byte, v uint64) []byte {
	var raw [8]byte
	for i := 7; i >= 0; i-- {
		raw[i] = byte(v)
		v >>= 8
	}
	var enc [vectorIDHexLen]byte
	hex.Encode(enc[:], raw[:])
	return append(buf, enc[:]...)
}

// hexSegment hex-encodes an arbitrary caller-supplied string (a metadata
// field name or value) for use as one '/'-delimited key segment.
//
// Metadata field and value strings are unconstrained -- unlike a namespace
// name (see ValidNamespaceName), nothing refuses one containing '/' or NUL.
// Hex-encoding closes both problems the same way appendIDHex closes it for
// IDs: the encoded segment can never contain '/' (so it cannot be mistaken
// for a segment boundary) and never contains NUL (so it stays writable on
// every backend, objstore included). It is an EQUALITY encoding only --
// docs/vectors/architecture.md calls this a "metadata equality index" -- so
// losing the original string's lexicographic order costs nothing: a lookup
// hex-encodes the same field and value and gets back the same segment.
func hexSegment(s string) string {
	if s == "" {
		// hex.EncodeToString of the empty string is itself the empty
		// string, which would leave an empty path segment between two '/'
		// separators -- "__vec:md/ns/<field>//<id>" for an empty metadata
		// VALUE. go-objstore's local provider refuses a key containing "//"
		// (found fixing go-qrdb vector-replicated review B4, the empty-value
		// metadata-index case, on the objstore leg of the backend matrix).
		// "-" can never collide with a real encoding: hex.EncodeToString's
		// alphabet is exactly [0-9a-f].
		return "-"
	}
	return hex.EncodeToString([]byte(s))
}
