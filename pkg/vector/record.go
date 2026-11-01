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

package vector

import (
	"encoding/binary"
	"math"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// DistanceMetric names how a namespace scores similarity between two
// embeddings. It is stored as a single byte, both on the wire
// (protocol.VectorNamespaceCreateParams.DistanceMetric) and in a namespace
// record.
type DistanceMetric = byte

// The distance metrics a namespace may be created with. Cosine is the
// default (docs/vectors/architecture.md "Shape chosen").
const (
	DistanceCosine     DistanceMetric = 0
	DistanceEuclidean  DistanceMetric = 1
	DistanceDotProduct DistanceMetric = 2
	DistanceManhattan  DistanceMetric = 3
)

// distanceMetrics is the set of DistanceMetric values this package recognises.
var distanceMetrics = map[DistanceMetric]struct{}{
	DistanceCosine:     {},
	DistanceEuclidean:  {},
	DistanceDotProduct: {},
	DistanceManhattan:  {},
}

// ValidDistanceMetric reports whether m is one this package recognises.
func ValidDistanceMetric(m DistanceMetric) bool {
	_, ok := distanceMetrics[m]
	return ok
}

// metricFeatures maps each DistanceMetric value added after the vector
// engine's first release to the cluster feature that admits it. A binary from
// before the value refuses it as unknown while its up-to-date peers accept
// it, so a record carrying it is proposed only once every replica of the
// group advertises the feature.
var metricFeatures = map[DistanceMetric]feature.Feature{
	DistanceManhattan: featuregate.VectorManhattan,
}

// MetricFeature returns the cluster feature every replica of a group must
// support before a namespace record carrying m is proposed to it, and false
// for a metric every vector-engine binary applies.
func MetricFeature(m DistanceMetric) (feature.Feature, bool) {
	f, ok := metricFeatures[m]
	return f, ok
}

// IndexType names the ANN index structure a namespace maintains. It is
// stored as a single byte, both on the wire and in a namespace record.
type IndexType = byte

// The index types a namespace may be created with. HNSW is the default
// (docs/vectors/architecture.md "Shape chosen").
const (
	IndexHNSW    IndexType = 0
	IndexFlat    IndexType = 1
	IndexIVFFlat IndexType = 2
)

// indexTypes is the set of IndexType values this package recognises.
var indexTypes = map[IndexType]struct{}{
	IndexHNSW:    {},
	IndexFlat:    {},
	IndexIVFFlat: {},
}

// ValidIndexType reports whether t is one this package recognises.
func ValidIndexType(t IndexType) bool {
	_, ok := indexTypes[t]
	return ok
}

// recordFormatVersion is the first byte of every encoded NamespaceRecord and
// VectorRecord, so a future format change can tell its own records apart
// from an earlier version's without guessing from length.
const recordFormatVersion = 1

// NamespaceRecord is the value stored at vector.NamespaceKey(name): a
// namespace's fixed geometry together with the mutable counters its apply
// handlers advance on every command (docs/vectors/engineering.md "Target:
// the vector engine").
type NamespaceRecord struct {
	Dimension          uint32
	DistanceMetric     DistanceMetric
	IndexType          IndexType
	IndexParams        map[string]string
	Seq                uint64
	NextID             uint64
	TrainingGeneration uint64
	CreatedAtUnixNano  int64
}

// EncodeNamespaceRecord serializes r.
func EncodeNamespaceRecord(r NamespaceRecord) ([]byte, error) {
	paramsSize, err := paramBlockSize(r.IndexParams)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 1+4+1+1+8+8+8+8+paramsSize)
	off := 0
	buf[off] = recordFormatVersion
	off++
	binary.LittleEndian.PutUint32(buf[off:], r.Dimension)
	off += 4
	buf[off] = r.DistanceMetric
	off++
	buf[off] = r.IndexType
	off++
	binary.LittleEndian.PutUint64(buf[off:], r.Seq)
	off += 8
	binary.LittleEndian.PutUint64(buf[off:], r.NextID)
	off += 8
	binary.LittleEndian.PutUint64(buf[off:], r.TrainingGeneration)
	off += 8
	binary.LittleEndian.PutUint64(buf[off:], uint64(r.CreatedAtUnixNano))
	off += 8
	if _, err := writeParamBlock(buf, off, r.IndexParams); err != nil {
		return nil, err
	}
	return buf, nil
}

// DecodeNamespaceRecord deserializes a NamespaceRecord.
func DecodeNamespaceRecord(data []byte) (NamespaceRecord, error) {
	if len(data) < 1 {
		return NamespaceRecord{}, &InvalidRecordError{Record: "namespace", Reason: "empty"}
	}
	if data[0] != recordFormatVersion {
		return NamespaceRecord{}, &UnsupportedRecordVersionError{Record: "namespace", Version: data[0]}
	}
	off := 1
	// Header layout is 38 bytes: dimension (4 bytes), distanceMetric (1),
	// indexType (1), seq (8), nextID (8), trainingGeneration (8), createdAt (8).
	if off+38 > len(data) {
		return NamespaceRecord{}, &InvalidRecordError{Record: "namespace", Reason: "truncated header"}
	}
	dimension := binary.LittleEndian.Uint32(data[off:])
	off += 4
	distanceMetric := data[off]
	off++
	indexType := data[off]
	off++
	seq := binary.LittleEndian.Uint64(data[off:])
	off += 8
	nextID := binary.LittleEndian.Uint64(data[off:])
	off += 8
	trainingGen := binary.LittleEndian.Uint64(data[off:])
	off += 8
	createdAt := int64(binary.LittleEndian.Uint64(data[off:]))
	off += 8
	params, _, err := readParamBlock(data, off, "namespace")
	if err != nil {
		return NamespaceRecord{}, err
	}
	return NamespaceRecord{
		Dimension:          dimension,
		DistanceMetric:     distanceMetric,
		IndexType:          indexType,
		IndexParams:        params,
		Seq:                seq,
		NextID:             nextID,
		TrainingGeneration: trainingGen,
		CreatedAtUnixNano:  createdAt,
	}, nil
}

// VectorRecord is the value stored at vector.VectorKey(namespace, id).
type VectorRecord struct {
	Embedding         []float32
	Metadata          map[string]string
	CreatedAtUnixNano int64
	UpdatedAtUnixNano int64
}

// EncodeVectorRecord serializes r.
func EncodeVectorRecord(r VectorRecord) ([]byte, error) {
	metaSize, err := paramBlockSize(r.Metadata)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, 1+4+len(r.Embedding)*4+metaSize+8+8)
	off := 0
	buf[off] = recordFormatVersion
	off++
	binary.LittleEndian.PutUint32(buf[off:], uint32(len(r.Embedding)))
	off += 4
	for _, f := range r.Embedding {
		binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f))
		off += 4
	}
	if off, err = writeParamBlock(buf, off, r.Metadata); err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint64(buf[off:], uint64(r.CreatedAtUnixNano))
	off += 8
	binary.LittleEndian.PutUint64(buf[off:], uint64(r.UpdatedAtUnixNano))
	return buf, nil
}

// DecodeVectorRecord deserializes a VectorRecord.
func DecodeVectorRecord(data []byte) (VectorRecord, error) {
	if len(data) < 1 {
		return VectorRecord{}, &InvalidRecordError{Record: "vector", Reason: "empty"}
	}
	if data[0] != recordFormatVersion {
		return VectorRecord{}, &UnsupportedRecordVersionError{Record: "vector", Version: data[0]}
	}
	off := 1
	if off+4 > len(data) {
		return VectorRecord{}, &InvalidRecordError{Record: "vector", Reason: "truncated embedding length"}
	}
	dim := int(binary.LittleEndian.Uint32(data[off:]))
	off += 4
	if off+dim*4 > len(data) {
		return VectorRecord{}, &InvalidRecordError{Record: "vector", Reason: "embedding exceeds record"}
	}
	embedding := make([]float32, dim)
	for i := 0; i < dim; i++ {
		embedding[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[off:]))
		off += 4
	}
	metadata, off, err := readParamBlock(data, off, "vector")
	if err != nil {
		return VectorRecord{}, err
	}
	if off+16 > len(data) {
		return VectorRecord{}, &InvalidRecordError{Record: "vector", Reason: "truncated timestamps"}
	}
	createdAt := int64(binary.LittleEndian.Uint64(data[off:]))
	off += 8
	updatedAt := int64(binary.LittleEndian.Uint64(data[off:]))
	return VectorRecord{
		Embedding:         embedding,
		Metadata:          metadata,
		CreatedAtUnixNano: createdAt,
		UpdatedAtUnixNano: updatedAt,
	}, nil
}

// MutationLogEntry is the value stored at
// vector.MutationLogKey(namespace, seq).
type MutationLogEntry struct {
	Op MutationOp
	ID uint64
}

// EncodeMutationLogEntry serializes e.
func EncodeMutationLogEntry(e MutationLogEntry) []byte {
	buf := make([]byte, 9)
	buf[0] = e.Op
	binary.LittleEndian.PutUint64(buf[1:], e.ID)
	return buf
}

// DecodeMutationLogEntry deserializes a MutationLogEntry.
func DecodeMutationLogEntry(data []byte) (MutationLogEntry, error) {
	if len(data) != 9 {
		return MutationLogEntry{}, &InvalidRecordError{Record: "mutation-log", Reason: "wrong length"}
	}
	return MutationLogEntry{Op: data[0], ID: binary.LittleEndian.Uint64(data[1:])}, nil
}

// paramBlockSize returns the encoded size of a count-prefixed run of
// length-prefixed (key, value) string pairs. Namespace index parameters and
// vector metadata both share this exact shape -- the same one
// protocol.ParamBlockSize validates for a command's wire payload, and the
// one definition this package delegates to rather than keeping its own
// (go-qrdb vector-replicated review, N6): two independent copies is how one
// of them stops sorting its keys before encoding and nobody notices (see
// protocol.WriteParamBlock).
func paramBlockSize(params map[string]string) (int, error) {
	return protocol.ParamBlockSize(params)
}

// writeParamBlock writes a count-prefixed run of (key, value) string pairs,
// in ascending key order, at buf[off:] and returns the offset past it. See
// protocol.WriteParamBlock for why the order matters: a record whose bytes
// depend on Go's randomised map iteration order is a record every replica
// may store differently from the same logical value.
func writeParamBlock(buf []byte, off int, params map[string]string) (int, error) {
	return protocol.WriteParamBlock(buf, off, params)
}

// readParamBlock reads a count-prefixed run of (key, value) string pairs from
// data[off:] and returns it with the offset past it.
func readParamBlock(data []byte, off int, record string) (map[string]string, int, error) {
	params, off, err := protocol.ReadParamBlock(data, off, record)
	if err != nil {
		return nil, 0, &InvalidRecordError{Record: record, Reason: err.Error()}
	}
	return params, off, nil
}

// ValidNamespaceName reports whether name is a legal vector namespace name:
// an RFC 1123 DNS label -- 1 to 63 characters, lowercase ASCII letters,
// digits, and hyphens, never leading or trailing with a hyphen -- exactly
// what docs/vectors/engineering.md has always specified for a namespace
// name, enforced here rather than merely documented (go-qrdb
// vector-replicated review, N3: the previous check accepted anything free of
// '/' and NUL, which is necessary for the key layout in keys.go but far
// looser than "DNS-label"). A name meeting this rule can never contain '/'
// or NUL either, so the key-layout property the old check existed for still
// holds, as a consequence rather than the whole rule.
func ValidNamespaceName(name string) bool {
	if len(name) == 0 || len(name) > 63 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-':
			if i == 0 || i == len(name)-1 {
				return false // a label never leads or trails with a hyphen
			}
		default:
			return false
		}
	}
	return true
}
