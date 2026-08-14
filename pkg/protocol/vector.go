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

package protocol

import (
	"encoding/binary"
	"math"
)

// ---------------------------------------------------------------------------
// Vector op codes (70-89)
// ---------------------------------------------------------------------------

const (
	// OpVectorUpsert stores or updates a vector in a namespace.
	// Payload: [nsLen:2 LE][namespace][idLen:2 LE][id][dim:4 LE][vector:dim*4 bytes float32][metaLen:4 LE][metadata].
	OpVectorUpsert byte = 70

	// OpVectorDelete removes a vector from a namespace.
	// Payload: [nsLen:2 LE][namespace][idLen:2 LE][id].
	OpVectorDelete byte = 71

	// OpVectorBatchUpsert stores multiple vectors in a single namespace.
	// Payload: [nsLen:2 LE][namespace][count:2 LE][vectors...]
	// Each vector: [idLen:2 LE][id][dim:4 LE][vector:dim*4 float32 LE][metaLen:4 LE][metadata].
	OpVectorBatchUpsert byte = 72

	// OpVectorBatchDelete removes multiple vectors from a namespace.
	// Payload: [nsLen:2 LE][namespace][count:2 LE][ids...]
	// Each id: [idLen:2 LE][id].
	OpVectorBatchDelete byte = 73

	// OpNamespaceCreate creates a new vector namespace.
	// Payload: [nsLen:2 LE][namespace][dimension:4 LE][distMetric:1][indexType:1].
	OpNamespaceCreate byte = 74

	// OpNamespaceDelete removes a vector namespace.
	// Payload: [nsLen:2 LE][namespace].
	OpNamespaceDelete byte = 75

	// OpVectorUpdateMetadata updates a vector's metadata without modifying the vector data.
	// Payload: [nsLen:2 LE][namespace][idLen:2 LE][id][metaLen:4 LE][metadata].
	OpVectorUpdateMetadata byte = 76

	// OpVectorRebuildIndex triggers an index rebuild for a namespace.
	// Payload: [nsLen:2 LE][namespace].
	OpVectorRebuildIndex byte = 77

	// OpVectorCompact triggers compaction for a namespace.
	// Payload: [nsLen:2 LE][namespace].
	OpVectorCompact byte = 78
)

// VectorUpsertParams holds decoded parameters for a single vector upsert
// operation, used both standalone and within batch upserts.
type VectorUpsertParams struct {
	ID       string
	Vector   []float32
	Metadata []byte
}

// ---------------------------------------------------------------------------
// Vector encoders
// ---------------------------------------------------------------------------

// EncodeVectorUpsert creates a binary Vector Upsert command.
// Format: [op:1][nsLen:2 LE][namespace][idLen:2 LE][id][dim:4 LE][vector:dim*4 float32 LE][metaLen:4 LE][metadata].
func EncodeVectorUpsert(namespace, id string, vector []float32, metadata []byte) []byte {
	nsBytes := []byte(namespace)
	idBytes := []byte(id)
	dim := len(vector)

	size := headerSize + 2 + len(nsBytes) + 2 + len(idBytes) + 4 + dim*4 + 4 + len(metadata)
	buf := make([]byte, size)
	off := 0

	buf[off] = OpVectorUpsert
	off++

	binary.LittleEndian.PutUint16(buf[off:], uint16(len(nsBytes)))
	off += 2
	copy(buf[off:], nsBytes)
	off += len(nsBytes)

	binary.LittleEndian.PutUint16(buf[off:], uint16(len(idBytes)))
	off += 2
	copy(buf[off:], idBytes)
	off += len(idBytes)

	binary.LittleEndian.PutUint32(buf[off:], uint32(dim))
	off += 4
	for _, f := range vector {
		binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f))
		off += 4
	}

	binary.LittleEndian.PutUint32(buf[off:], uint32(len(metadata)))
	off += 4
	copy(buf[off:], metadata)

	return buf
}

// EncodeVectorDelete creates a binary Vector Delete command.
// Format: [op:1][nsLen:2 LE][namespace][idLen:2 LE][id].
func EncodeVectorDelete(namespace, id string) []byte {
	nsBytes := []byte(namespace)
	idBytes := []byte(id)

	buf := make([]byte, headerSize+2+len(nsBytes)+2+len(idBytes))
	off := 0

	buf[off] = OpVectorDelete
	off++

	binary.LittleEndian.PutUint16(buf[off:], uint16(len(nsBytes)))
	off += 2
	copy(buf[off:], nsBytes)
	off += len(nsBytes)

	binary.LittleEndian.PutUint16(buf[off:], uint16(len(idBytes)))
	off += 2
	copy(buf[off:], idBytes)

	return buf
}

// EncodeNamespaceCreate creates a binary Namespace Create command.
// Format: [op:1][nsLen:2 LE][namespace][dimension:4 LE][distMetric:1][indexType:1].
func EncodeNamespaceCreate(namespace string, dimension uint32, distMetric, indexType byte) []byte {
	nsBytes := []byte(namespace)

	buf := make([]byte, headerSize+2+len(nsBytes)+4+1+1)
	off := 0

	buf[off] = OpNamespaceCreate
	off++

	binary.LittleEndian.PutUint16(buf[off:], uint16(len(nsBytes)))
	off += 2
	copy(buf[off:], nsBytes)
	off += len(nsBytes)

	binary.LittleEndian.PutUint32(buf[off:], dimension)
	off += 4

	buf[off] = distMetric
	off++

	buf[off] = indexType

	return buf
}

// EncodeNamespaceDelete creates a binary Namespace Delete command.
// Format: [op:1][nsLen:2 LE][namespace].
func EncodeNamespaceDelete(namespace string) []byte {
	nsBytes := []byte(namespace)

	buf := make([]byte, headerSize+2+len(nsBytes))
	buf[0] = OpNamespaceDelete
	binary.LittleEndian.PutUint16(buf[1:3], uint16(len(nsBytes)))
	copy(buf[3:], nsBytes)

	return buf
}

// EncodeVectorBatchUpsert creates a binary Vector Batch Upsert command.
// Format: [op:1][nsLen:2 LE][namespace][count:2 LE][vectors...]
// Each vector: [idLen:2 LE][id][dim:4 LE][vector:dim*4 float32 LE][metaLen:4 LE][metadata].
func EncodeVectorBatchUpsert(namespace string, vectors []VectorUpsertParams) []byte {
	nsBytes := []byte(namespace)

	// Calculate total size.
	size := headerSize + 2 + len(nsBytes) + 2
	for _, v := range vectors {
		idBytes := []byte(v.ID)
		size += 2 + len(idBytes) + 4 + len(v.Vector)*4 + 4 + len(v.Metadata)
	}

	buf := make([]byte, size)
	off := 0

	buf[off] = OpVectorBatchUpsert
	off++

	binary.LittleEndian.PutUint16(buf[off:], uint16(len(nsBytes)))
	off += 2
	copy(buf[off:], nsBytes)
	off += len(nsBytes)

	binary.LittleEndian.PutUint16(buf[off:], uint16(len(vectors)))
	off += 2

	for _, v := range vectors {
		idBytes := []byte(v.ID)

		binary.LittleEndian.PutUint16(buf[off:], uint16(len(idBytes)))
		off += 2
		copy(buf[off:], idBytes)
		off += len(idBytes)

		binary.LittleEndian.PutUint32(buf[off:], uint32(len(v.Vector)))
		off += 4
		for _, f := range v.Vector {
			binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f))
			off += 4
		}

		binary.LittleEndian.PutUint32(buf[off:], uint32(len(v.Metadata)))
		off += 4
		copy(buf[off:], v.Metadata)
		off += len(v.Metadata)
	}

	return buf
}

// ---------------------------------------------------------------------------
// Vector decoders
// ---------------------------------------------------------------------------

// DecodeVectorUpsert extracts namespace, id, vector, and metadata from a
// Vector Upsert payload.
func DecodeVectorUpsert(payload []byte) (namespace, id string, vector []float32, metadata []byte, err error) {
	if len(payload) < 2 {
		return "", "", nil, nil, &InvalidCommandError{Reason: "vector upsert: payload too short"}
	}

	off := 0

	nsLen := int(binary.LittleEndian.Uint16(payload[off:]))
	off += 2
	if off+nsLen > len(payload) {
		return "", "", nil, nil, &InvalidCommandError{Reason: "vector upsert: namespace length exceeds payload"}
	}
	namespace = string(payload[off : off+nsLen])
	off += nsLen

	if off+2 > len(payload) {
		return "", "", nil, nil, &InvalidCommandError{Reason: "vector upsert: missing id length"}
	}
	idLen := int(binary.LittleEndian.Uint16(payload[off:]))
	off += 2
	if off+idLen > len(payload) {
		return "", "", nil, nil, &InvalidCommandError{Reason: "vector upsert: id length exceeds payload"}
	}
	id = string(payload[off : off+idLen])
	off += idLen

	if off+4 > len(payload) {
		return "", "", nil, nil, &InvalidCommandError{Reason: "vector upsert: missing dimension"}
	}
	dim := int(binary.LittleEndian.Uint32(payload[off:]))
	off += 4

	if off+dim*4 > len(payload) {
		return "", "", nil, nil, &InvalidCommandError{Reason: "vector upsert: vector data exceeds payload"}
	}
	vector = make([]float32, dim)
	for i := 0; i < dim; i++ {
		vector[i] = math.Float32frombits(binary.LittleEndian.Uint32(payload[off:]))
		off += 4
	}

	if off+4 > len(payload) {
		return "", "", nil, nil, &InvalidCommandError{Reason: "vector upsert: missing metadata length"}
	}
	metaLen := int(binary.LittleEndian.Uint32(payload[off:]))
	off += 4
	if off+metaLen > len(payload) {
		return "", "", nil, nil, &InvalidCommandError{Reason: "vector upsert: metadata exceeds payload"}
	}
	metadata = payload[off : off+metaLen]

	return namespace, id, vector, metadata, nil
}

// DecodeVectorDelete extracts namespace and id from a Vector Delete payload.
func DecodeVectorDelete(payload []byte) (namespace, id string, err error) {
	if len(payload) < 2 {
		return "", "", &InvalidCommandError{Reason: "vector delete: payload too short"}
	}

	off := 0

	nsLen := int(binary.LittleEndian.Uint16(payload[off:]))
	off += 2
	if off+nsLen > len(payload) {
		return "", "", &InvalidCommandError{Reason: "vector delete: namespace length exceeds payload"}
	}
	namespace = string(payload[off : off+nsLen])
	off += nsLen

	if off+2 > len(payload) {
		return "", "", &InvalidCommandError{Reason: "vector delete: missing id length"}
	}
	idLen := int(binary.LittleEndian.Uint16(payload[off:]))
	off += 2
	if off+idLen > len(payload) {
		return "", "", &InvalidCommandError{Reason: "vector delete: id length exceeds payload"}
	}
	id = string(payload[off : off+idLen])

	return namespace, id, nil
}

// DecodeNamespaceCreate extracts namespace, dimension, distance metric, and
// index type from a Namespace Create payload.
func DecodeNamespaceCreate(payload []byte) (namespace string, dimension uint32, distMetric, indexType byte, err error) {
	if len(payload) < 2 {
		return "", 0, 0, 0, &InvalidCommandError{Reason: "namespace create: payload too short"}
	}

	off := 0

	nsLen := int(binary.LittleEndian.Uint16(payload[off:]))
	off += 2
	if off+nsLen > len(payload) {
		return "", 0, 0, 0, &InvalidCommandError{Reason: "namespace create: namespace length exceeds payload"}
	}
	namespace = string(payload[off : off+nsLen])
	off += nsLen

	if off+6 > len(payload) {
		return "", 0, 0, 0, &InvalidCommandError{Reason: "namespace create: missing dimension/metric/type"}
	}
	dimension = binary.LittleEndian.Uint32(payload[off:])
	off += 4

	distMetric = payload[off]
	off++

	indexType = payload[off]

	return namespace, dimension, distMetric, indexType, nil
}

// DecodeNamespaceDelete extracts the namespace from a Namespace Delete payload.
func DecodeNamespaceDelete(payload []byte) (string, error) {
	if len(payload) < 2 {
		return "", &InvalidCommandError{Reason: "namespace delete: payload too short"}
	}
	nsLen := int(binary.LittleEndian.Uint16(payload[0:2]))
	if 2+nsLen > len(payload) {
		return "", &InvalidCommandError{Reason: "namespace delete: namespace length exceeds payload"}
	}
	return string(payload[2 : 2+nsLen]), nil
}

// DecodeVectorBatchUpsert extracts namespace and a slice of VectorUpsertParams
// from a Vector Batch Upsert payload.
func DecodeVectorBatchUpsert(payload []byte) (string, []VectorUpsertParams, error) {
	if len(payload) < 2 {
		return "", nil, &InvalidCommandError{Reason: "vector batch upsert: payload too short"}
	}

	off := 0

	nsLen := int(binary.LittleEndian.Uint16(payload[off:]))
	off += 2
	if off+nsLen > len(payload) {
		return "", nil, &InvalidCommandError{Reason: "vector batch upsert: namespace length exceeds payload"}
	}
	namespace := string(payload[off : off+nsLen])
	off += nsLen

	if off+2 > len(payload) {
		return "", nil, &InvalidCommandError{Reason: "vector batch upsert: missing vector count"}
	}
	count := int(binary.LittleEndian.Uint16(payload[off:]))
	off += 2

	vectors := make([]VectorUpsertParams, 0, count)
	for i := 0; i < count; i++ {
		if off+2 > len(payload) {
			return "", nil, &InvalidCommandError{Reason: "vector batch upsert: truncated vector id length"}
		}
		idLen := int(binary.LittleEndian.Uint16(payload[off:]))
		off += 2
		if off+idLen > len(payload) {
			return "", nil, &InvalidCommandError{Reason: "vector batch upsert: id length exceeds payload"}
		}
		id := string(payload[off : off+idLen])
		off += idLen

		if off+4 > len(payload) {
			return "", nil, &InvalidCommandError{Reason: "vector batch upsert: missing dimension"}
		}
		dim := int(binary.LittleEndian.Uint32(payload[off:]))
		off += 4

		if off+dim*4 > len(payload) {
			return "", nil, &InvalidCommandError{Reason: "vector batch upsert: vector data exceeds payload"}
		}
		vec := make([]float32, dim)
		for j := 0; j < dim; j++ {
			vec[j] = math.Float32frombits(binary.LittleEndian.Uint32(payload[off:]))
			off += 4
		}

		if off+4 > len(payload) {
			return "", nil, &InvalidCommandError{Reason: "vector batch upsert: missing metadata length"}
		}
		metaLen := int(binary.LittleEndian.Uint32(payload[off:]))
		off += 4
		if off+metaLen > len(payload) {
			return "", nil, &InvalidCommandError{Reason: "vector batch upsert: metadata exceeds payload"}
		}
		meta := payload[off : off+metaLen]
		off += metaLen

		vectors = append(vectors, VectorUpsertParams{
			ID:       id,
			Vector:   vec,
			Metadata: meta,
		})
	}

	return namespace, vectors, nil
}
