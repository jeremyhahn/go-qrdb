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
	"sort"
)

// ---------------------------------------------------------------------------
// Vector operations (70-79).
//
// Every vector write is a command in this extended protocol, proposed to the
// database binding's host and applied by the engine over storage.Storage,
// exactly like every other command in this file -- see
// docs/vectors/engineering.md "Target: the vector engine". Opcode numbers are
// assigned here, once, and are wire format from then on.
//
// A timestamp a record needs (creation, update) travels IN the payload,
// supplied by the proposer, rather than being read from the applying
// replica's clock: every replica must apply the identical record from the
// identical log entry (docs/vectors/engineering.md "Target: determinism").
// ---------------------------------------------------------------------------

const (
	// OpVectorNamespaceCreate creates a vector namespace with fixed geometry.
	// Payload: [nameLen:2 LE][name][dimension:4 LE][distanceMetric:1][indexType:1]
	// [createdAtUnixNano:8 LE][paramCount:2 LE]([keyLen:2 LE][key][valLen:2 LE][val])*.
	OpVectorNamespaceCreate byte = 70

	// OpVectorNamespaceDelete deletes a vector namespace and everything under
	// it. Payload: [nameLen:2 LE][name].
	OpVectorNamespaceDelete byte = 71

	// OpVectorUpsert inserts or replaces one vector. Payload:
	// [nameLen:2 LE][name][timestampUnixNano:8 LE][hasID:1][id:8 LE]
	// [embeddingLen:4 LE][embedding: embeddingLen*4 bytes float32 LE each]
	// [metaCount:2 LE]([keyLen:2 LE][key][valLen:2 LE][val])*.
	OpVectorUpsert byte = 72

	// OpVectorBatchUpsert inserts or replaces several vectors atomically.
	// Payload: [nameLen:2 LE][name][timestampUnixNano:8 LE][count:2 LE](
	// [hasID:1][id:8 LE][embeddingLen:4 LE][embedding...][metaCount:2 LE]
	// ([keyLen:2 LE][key][valLen:2 LE][val])*)*.
	OpVectorBatchUpsert byte = 73

	// OpVectorDelete removes one vector by ID. Payload:
	// [nameLen:2 LE][name][id:8 LE].
	OpVectorDelete byte = 74

	// OpVectorBatchDelete removes several vectors by ID atomically. Payload:
	// [nameLen:2 LE][name][count:2 LE][id:8 LE]*.
	OpVectorBatchDelete byte = 75

	// OpVectorUpdateMetadata replaces a vector's metadata without touching its
	// embedding. Payload: [nameLen:2 LE][name][id:8 LE][timestampUnixNano:8 LE]
	// [metaCount:2 LE]([keyLen:2 LE][key][valLen:2 LE][val])*.
	OpVectorUpdateMetadata byte = 76

	// OpVectorIndexReconfigure records new index parameters and advances the
	// namespace's training generation; each replica rebuilds its own index
	// locally from the result. Payload: [nameLen:2 LE][name][indexType:1]
	// [paramCount:2 LE]([keyLen:2 LE][key][valLen:2 LE][val])*.
	OpVectorIndexReconfigure byte = 77
)

// VectorNamespaceCreateParams is the decoded payload of OpVectorNamespaceCreate.
type VectorNamespaceCreateParams struct {
	Name              string
	Dimension         uint32
	DistanceMetric    byte
	IndexType         byte
	CreatedAtUnixNano int64
	IndexParams       map[string]string
}

// VectorUpsertParams is the decoded payload of OpVectorUpsert, and one element
// of OpVectorBatchUpsert.
type VectorUpsertParams struct {
	HasID     bool
	ID        uint64
	Embedding []float32
	Metadata  map[string]string
}

// VectorBatchUpsertParams is the decoded payload of OpVectorBatchUpsert.
type VectorBatchUpsertParams struct {
	Name              string
	TimestampUnixNano int64
	Vectors           []VectorUpsertParams
}

// VectorUpdateMetadataParams is the decoded payload of OpVectorUpdateMetadata.
type VectorUpdateMetadataParams struct {
	Name              string
	ID                uint64
	TimestampUnixNano int64
	Metadata          map[string]string
}

// VectorIndexReconfigureParams is the decoded payload of
// OpVectorIndexReconfigure.
type VectorIndexReconfigureParams struct {
	Name        string
	IndexType   byte
	IndexParams map[string]string
}

// vectorNameSize returns the encoded size of a length-prefixed namespace
// name, validating that it fits the two-byte field that frames it.
func vectorNameSize(name string) (int, error) {
	if err := checkUint16Len(len(name), "namespace name"); err != nil {
		return 0, err
	}
	return 2 + len(name), nil
}

// writeVectorName writes a length-prefixed namespace name at buf[off:] and
// returns the offset past it.
func writeVectorName(buf []byte, off int, name string) (int, error) {
	if err := putUint16Len(buf[off:], len(name), "namespace name"); err != nil {
		return 0, err
	}
	off += 2
	copy(buf[off:], name)
	return off + len(name), nil
}

// readVectorName reads a length-prefixed namespace name from payload[off:]
// and returns it with the offset past it.
func readVectorName(payload []byte, off int, reason string) (string, int, error) {
	if off+2 > len(payload) {
		return "", 0, &InvalidCommandError{Reason: reason + ": truncated name length"}
	}
	nameLen := int(binary.LittleEndian.Uint16(payload[off:]))
	off += 2
	if off+nameLen > len(payload) {
		return "", 0, &InvalidCommandError{Reason: reason + ": name exceeds payload"}
	}
	return string(payload[off : off+nameLen]), off + nameLen, nil
}

// ParamBlockSize returns the encoded size of a count-prefixed run of
// length-prefixed (key, value) string pairs. Metadata and index-parameter
// maps share this exact shape time-series tags do (tagBlockSize), but are
// validated under their own field names ("param key"/"param value") rather
// than delegating to it, so a refusal names the vector field that overflowed
// instead of reading like a time-series one.
//
// Exported, and the single definition pkg/vector's namespace and vector
// RECORD codec calls too (go-qrdb vector-replicated review, N6): a command's
// wire payload and a record's stored bytes share this exact param-block
// shape, and two independent implementations of "sort before encoding" is
// how one of them stops sorting and nobody notices (see WriteParamBlock).
func ParamBlockSize(params map[string]string) (int, error) {
	if err := checkUint16Len(len(params), "param count"); err != nil {
		return 0, err
	}
	size := 2
	for k, v := range params {
		if err := checkUint16Len(len(k), "param key"); err != nil {
			return 0, err
		}
		if err := checkUint16Len(len(v), "param value"); err != nil {
			return 0, err
		}
		var err error
		if size, err = addSizes(size, 2, len(k), 2, len(v)); err != nil {
			return 0, err
		}
	}
	return size, nil
}

// WriteParamBlock writes a count-prefixed run of (key, value) string pairs at
// buf[off:] and returns the offset past it.
//
// Pairs are written in ASCENDING KEY ORDER, not map iteration order. Go
// deliberately randomises map iteration per process, so two encodings of the
// identical map -- the exact situation every replica applying the same
// committed command is in -- produced different byte strings: 47 of 50
// repeated encodings of one 16-entry map differed in a direct measurement
// (go-qrdb vector-replicated review, B1). A byte-level comparison (OpGuarded,
// a CAS on a record's raw bytes) then disagrees with itself across replicas,
// and the stored record a replica reads back is not the one any other
// replica would have written from the same logical value. Sorting is the
// entire fix: every replica derives the same key order from the same map,
// with no coordination needed.
func WriteParamBlock(buf []byte, off int, params map[string]string) (int, error) {
	if err := putUint16Len(buf[off:], len(params), "param count"); err != nil {
		return 0, err
	}
	off += 2
	for _, k := range sortedParamKeys(params) {
		v := params[k]
		if err := putUint16Len(buf[off:], len(k), "param key"); err != nil {
			return 0, err
		}
		off += 2
		copy(buf[off:], k)
		off += len(k)

		if err := putUint16Len(buf[off:], len(v), "param value"); err != nil {
			return 0, err
		}
		off += 2
		copy(buf[off:], v)
		off += len(v)
	}
	return off, nil
}

// sortedParamKeys returns params' keys in ascending order, the order
// WriteParamBlock writes them in.
func sortedParamKeys(params map[string]string) []string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ReadParamBlock reads a count-prefixed run of (key, value) string pairs from
// payload[off:] and returns it with the offset past it.
func ReadParamBlock(payload []byte, off int, reason string) (map[string]string, int, error) {
	if off+2 > len(payload) {
		return nil, 0, &InvalidCommandError{Reason: reason + ": truncated param count"}
	}
	count := int(binary.LittleEndian.Uint16(payload[off:]))
	off += 2

	if count == 0 {
		return nil, off, nil
	}
	params := make(map[string]string, count)
	for i := 0; i < count; i++ {
		if off+2 > len(payload) {
			return nil, 0, &InvalidCommandError{Reason: reason + ": truncated param key length"}
		}
		kLen := int(binary.LittleEndian.Uint16(payload[off:]))
		off += 2
		if off+kLen > len(payload) {
			return nil, 0, &InvalidCommandError{Reason: reason + ": param key exceeds payload"}
		}
		k := string(payload[off : off+kLen])
		off += kLen

		if off+2 > len(payload) {
			return nil, 0, &InvalidCommandError{Reason: reason + ": truncated param value length"}
		}
		vLen := int(binary.LittleEndian.Uint16(payload[off:]))
		off += 2
		if off+vLen > len(payload) {
			return nil, 0, &InvalidCommandError{Reason: reason + ": param value exceeds payload"}
		}
		params[k] = string(payload[off : off+vLen])
		off += vLen
	}
	return params, off, nil
}

// embeddingSize returns the encoded size of a length-prefixed float32
// embedding: a four-byte element count followed by four bytes per element.
func embeddingSize(embedding []float32) (int, error) {
	if err := CheckUint32Len(len(embedding), "embedding"); err != nil {
		return 0, err
	}
	bytes, err := mulSize(len(embedding), 4)
	if err != nil {
		return 0, err
	}
	return addSizes(4, bytes)
}

// writeEmbedding writes a length-prefixed float32 embedding at buf[off:] and
// returns the offset past it.
func writeEmbedding(buf []byte, off int, embedding []float32) (int, error) {
	if err := putUint32Len(buf[off:], len(embedding), "embedding"); err != nil {
		return 0, err
	}
	off += 4
	for _, f := range embedding {
		binary.LittleEndian.PutUint32(buf[off:], math.Float32bits(f))
		off += 4
	}
	return off, nil
}

// readEmbedding reads a length-prefixed float32 embedding from payload[off:]
// and returns it with the offset past it.
func readEmbedding(payload []byte, off int, reason string) ([]float32, int, error) {
	if off+4 > len(payload) {
		return nil, 0, &InvalidCommandError{Reason: reason + ": truncated embedding length"}
	}
	dim := int(binary.LittleEndian.Uint32(payload[off:]))
	off += 4
	need, err := mulSize(dim, 4)
	if err != nil {
		return nil, 0, &InvalidCommandError{Reason: reason + ": embedding length overflow"}
	}
	if off+need > len(payload) {
		return nil, 0, &InvalidCommandError{Reason: reason + ": embedding exceeds payload"}
	}
	embedding := make([]float32, dim)
	for i := 0; i < dim; i++ {
		embedding[i] = math.Float32frombits(binary.LittleEndian.Uint32(payload[off:]))
		off += 4
	}
	return embedding, off, nil
}

// upsertItemSize returns the encoded size of one OpVectorBatchUpsert element:
// [hasID:1][id:8][embeddingLen:4][embedding...][metaCount:2][meta...].
func upsertItemSize(item VectorUpsertParams) (int, error) {
	embSize, err := embeddingSize(item.Embedding)
	if err != nil {
		return 0, err
	}
	metaSize, err := ParamBlockSize(item.Metadata)
	if err != nil {
		return 0, err
	}
	return addSizes(1, 8, embSize, metaSize)
}

// writeUpsertItem writes one OpVectorBatchUpsert element at buf[off:] and
// returns the offset past it.
func writeUpsertItem(buf []byte, off int, item VectorUpsertParams) (int, error) {
	if item.HasID {
		buf[off] = 1
	} else {
		buf[off] = 0
	}
	off++
	binary.LittleEndian.PutUint64(buf[off:], item.ID)
	off += 8
	var err error
	if off, err = writeEmbedding(buf, off, item.Embedding); err != nil {
		return 0, err
	}
	return WriteParamBlock(buf, off, item.Metadata)
}

// readUpsertItem reads one OpVectorBatchUpsert element from payload[off:] and
// returns it with the offset past it.
func readUpsertItem(payload []byte, off int, reason string) (VectorUpsertParams, int, error) {
	if off+9 > len(payload) {
		return VectorUpsertParams{}, 0, &InvalidCommandError{Reason: reason + ": truncated hasID/id"}
	}
	hasID := payload[off] != 0
	off++
	id := binary.LittleEndian.Uint64(payload[off:])
	off += 8

	embedding, off, err := readEmbedding(payload, off, reason)
	if err != nil {
		return VectorUpsertParams{}, 0, err
	}
	metadata, off, err := ReadParamBlock(payload, off, reason)
	if err != nil {
		return VectorUpsertParams{}, 0, err
	}
	return VectorUpsertParams{HasID: hasID, ID: id, Embedding: embedding, Metadata: metadata}, off, nil
}

// EncodeVectorNamespaceCreate creates a binary OpVectorNamespaceCreate command.
func EncodeVectorNamespaceCreate(p VectorNamespaceCreateParams) ([]byte, error) {
	nameSize, err := vectorNameSize(p.Name)
	if err != nil {
		return nil, err
	}
	paramSize, err := ParamBlockSize(p.IndexParams)
	if err != nil {
		return nil, err
	}
	size, err := addSizes(headerSize, nameSize, 4, 1, 1, 8, paramSize)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, size)
	off := 0
	buf[off] = OpVectorNamespaceCreate
	off++
	if off, err = writeVectorName(buf, off, p.Name); err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint32(buf[off:], p.Dimension)
	off += 4
	buf[off] = p.DistanceMetric
	off++
	buf[off] = p.IndexType
	off++
	binary.LittleEndian.PutUint64(buf[off:], uint64(p.CreatedAtUnixNano))
	off += 8
	if _, err = WriteParamBlock(buf, off, p.IndexParams); err != nil {
		return nil, err
	}
	return buf, nil
}

// DecodeVectorNamespaceCreate decodes an OpVectorNamespaceCreate payload (the
// bytes after the opcode).
func DecodeVectorNamespaceCreate(payload []byte) (VectorNamespaceCreateParams, error) {
	const reason = "vector namespace create"
	name, off, err := readVectorName(payload, 0, reason)
	if err != nil {
		return VectorNamespaceCreateParams{}, err
	}
	if off+14 > len(payload) {
		return VectorNamespaceCreateParams{}, &InvalidCommandError{Reason: reason + ": truncated geometry"}
	}
	dimension := binary.LittleEndian.Uint32(payload[off:])
	off += 4
	distanceMetric := payload[off]
	off++
	indexType := payload[off]
	off++
	createdAt := int64(binary.LittleEndian.Uint64(payload[off:]))
	off += 8
	params, _, err := ReadParamBlock(payload, off, reason)
	if err != nil {
		return VectorNamespaceCreateParams{}, err
	}
	return VectorNamespaceCreateParams{
		Name:              name,
		Dimension:         dimension,
		DistanceMetric:    distanceMetric,
		IndexType:         indexType,
		CreatedAtUnixNano: createdAt,
		IndexParams:       params,
	}, nil
}

// EncodeVectorNamespaceDelete creates a binary OpVectorNamespaceDelete command.
func EncodeVectorNamespaceDelete(name string) ([]byte, error) {
	nameSize, err := vectorNameSize(name)
	if err != nil {
		return nil, err
	}
	size, err := addSizes(headerSize, nameSize)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, size)
	buf[0] = OpVectorNamespaceDelete
	if _, err := writeVectorName(buf, headerSize, name); err != nil {
		return nil, err
	}
	return buf, nil
}

// DecodeVectorNamespaceDelete decodes an OpVectorNamespaceDelete payload.
func DecodeVectorNamespaceDelete(payload []byte) (string, error) {
	name, _, err := readVectorName(payload, 0, "vector namespace delete")
	return name, err
}

// EncodeVectorUpsert creates a binary OpVectorUpsert command.
func EncodeVectorUpsert(name string, timestampUnixNano int64, item VectorUpsertParams) ([]byte, error) {
	nameSize, err := vectorNameSize(name)
	if err != nil {
		return nil, err
	}
	itemSize, err := upsertItemSize(item)
	if err != nil {
		return nil, err
	}
	size, err := addSizes(headerSize, nameSize, 8, itemSize)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, size)
	off := 0
	buf[off] = OpVectorUpsert
	off++
	if off, err = writeVectorName(buf, off, name); err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint64(buf[off:], uint64(timestampUnixNano))
	off += 8
	if _, err = writeUpsertItem(buf, off, item); err != nil {
		return nil, err
	}
	return buf, nil
}

// DecodeVectorUpsert decodes an OpVectorUpsert payload.
func DecodeVectorUpsert(payload []byte) (name string, timestampUnixNano int64, item VectorUpsertParams, err error) {
	const reason = "vector upsert"
	name, off, err := readVectorName(payload, 0, reason)
	if err != nil {
		return "", 0, VectorUpsertParams{}, err
	}
	if off+8 > len(payload) {
		return "", 0, VectorUpsertParams{}, &InvalidCommandError{Reason: reason + ": truncated timestamp"}
	}
	timestampUnixNano = int64(binary.LittleEndian.Uint64(payload[off:]))
	off += 8
	item, _, err = readUpsertItem(payload, off, reason)
	if err != nil {
		return "", 0, VectorUpsertParams{}, err
	}
	return name, timestampUnixNano, item, nil
}

// EncodeVectorBatchUpsert creates a binary OpVectorBatchUpsert command.
func EncodeVectorBatchUpsert(p VectorBatchUpsertParams) ([]byte, error) {
	nameSize, err := vectorNameSize(p.Name)
	if err != nil {
		return nil, err
	}
	if err := checkUint16Len(len(p.Vectors), "vector count"); err != nil {
		return nil, err
	}
	size, err := addSizes(headerSize, nameSize, 8, 2)
	if err != nil {
		return nil, err
	}
	itemSizes := make([]int, len(p.Vectors))
	for i, item := range p.Vectors {
		itemSize, err := upsertItemSize(item)
		if err != nil {
			return nil, err
		}
		itemSizes[i] = itemSize
		if size, err = addSizes(size, itemSize); err != nil {
			return nil, err
		}
	}

	buf := make([]byte, size)
	off := 0
	buf[off] = OpVectorBatchUpsert
	off++
	if off, err = writeVectorName(buf, off, p.Name); err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint64(buf[off:], uint64(p.TimestampUnixNano))
	off += 8
	if err := putUint16Len(buf[off:], len(p.Vectors), "vector count"); err != nil {
		return nil, err
	}
	off += 2
	for _, item := range p.Vectors {
		if off, err = writeUpsertItem(buf, off, item); err != nil {
			return nil, err
		}
	}
	return buf, nil
}

// DecodeVectorBatchUpsert decodes an OpVectorBatchUpsert payload.
func DecodeVectorBatchUpsert(payload []byte) (VectorBatchUpsertParams, error) {
	const reason = "vector batch upsert"
	name, off, err := readVectorName(payload, 0, reason)
	if err != nil {
		return VectorBatchUpsertParams{}, err
	}
	if off+8 > len(payload) {
		return VectorBatchUpsertParams{}, &InvalidCommandError{Reason: reason + ": truncated timestamp"}
	}
	timestampUnixNano := int64(binary.LittleEndian.Uint64(payload[off:]))
	off += 8
	if off+2 > len(payload) {
		return VectorBatchUpsertParams{}, &InvalidCommandError{Reason: reason + ": truncated vector count"}
	}
	count := int(binary.LittleEndian.Uint16(payload[off:]))
	off += 2

	vectors := make([]VectorUpsertParams, count)
	for i := 0; i < count; i++ {
		var item VectorUpsertParams
		item, off, err = readUpsertItem(payload, off, reason)
		if err != nil {
			return VectorBatchUpsertParams{}, err
		}
		vectors[i] = item
	}
	return VectorBatchUpsertParams{Name: name, TimestampUnixNano: timestampUnixNano, Vectors: vectors}, nil
}

// EncodeVectorDelete creates a binary OpVectorDelete command.
func EncodeVectorDelete(name string, id uint64) ([]byte, error) {
	nameSize, err := vectorNameSize(name)
	if err != nil {
		return nil, err
	}
	size, err := addSizes(headerSize, nameSize, 8)
	if err != nil {
		return nil, err
	}
	buf := make([]byte, size)
	off := 0
	buf[off] = OpVectorDelete
	off++
	if off, err = writeVectorName(buf, off, name); err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint64(buf[off:], id)
	return buf, nil
}

// DecodeVectorDelete decodes an OpVectorDelete payload.
func DecodeVectorDelete(payload []byte) (name string, id uint64, err error) {
	const reason = "vector delete"
	name, off, err := readVectorName(payload, 0, reason)
	if err != nil {
		return "", 0, err
	}
	if off+8 > len(payload) {
		return "", 0, &InvalidCommandError{Reason: reason + ": truncated id"}
	}
	return name, binary.LittleEndian.Uint64(payload[off:]), nil
}

// EncodeVectorBatchDelete creates a binary OpVectorBatchDelete command.
func EncodeVectorBatchDelete(name string, ids []uint64) ([]byte, error) {
	nameSize, err := vectorNameSize(name)
	if err != nil {
		return nil, err
	}
	if err := checkUint16Len(len(ids), "id count"); err != nil {
		return nil, err
	}
	idsSize, err := mulSize(len(ids), 8)
	if err != nil {
		return nil, err
	}
	size, err := addSizes(headerSize, nameSize, 2, idsSize)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, size)
	off := 0
	buf[off] = OpVectorBatchDelete
	off++
	if off, err = writeVectorName(buf, off, name); err != nil {
		return nil, err
	}
	if err := putUint16Len(buf[off:], len(ids), "id count"); err != nil {
		return nil, err
	}
	off += 2
	for _, id := range ids {
		binary.LittleEndian.PutUint64(buf[off:], id)
		off += 8
	}
	return buf, nil
}

// DecodeVectorBatchDelete decodes an OpVectorBatchDelete payload.
func DecodeVectorBatchDelete(payload []byte) (name string, ids []uint64, err error) {
	const reason = "vector batch delete"
	name, off, err := readVectorName(payload, 0, reason)
	if err != nil {
		return "", nil, err
	}
	if off+2 > len(payload) {
		return "", nil, &InvalidCommandError{Reason: reason + ": truncated id count"}
	}
	count := int(binary.LittleEndian.Uint16(payload[off:]))
	off += 2
	ids = make([]uint64, count)
	for i := 0; i < count; i++ {
		if off+8 > len(payload) {
			return "", nil, &InvalidCommandError{Reason: reason + ": truncated id"}
		}
		ids[i] = binary.LittleEndian.Uint64(payload[off:])
		off += 8
	}
	return name, ids, nil
}

// EncodeVectorUpdateMetadata creates a binary OpVectorUpdateMetadata command.
func EncodeVectorUpdateMetadata(p VectorUpdateMetadataParams) ([]byte, error) {
	nameSize, err := vectorNameSize(p.Name)
	if err != nil {
		return nil, err
	}
	metaSize, err := ParamBlockSize(p.Metadata)
	if err != nil {
		return nil, err
	}
	size, err := addSizes(headerSize, nameSize, 8, 8, metaSize)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, size)
	off := 0
	buf[off] = OpVectorUpdateMetadata
	off++
	if off, err = writeVectorName(buf, off, p.Name); err != nil {
		return nil, err
	}
	binary.LittleEndian.PutUint64(buf[off:], p.ID)
	off += 8
	binary.LittleEndian.PutUint64(buf[off:], uint64(p.TimestampUnixNano))
	off += 8
	if _, err = WriteParamBlock(buf, off, p.Metadata); err != nil {
		return nil, err
	}
	return buf, nil
}

// DecodeVectorUpdateMetadata decodes an OpVectorUpdateMetadata payload.
func DecodeVectorUpdateMetadata(payload []byte) (VectorUpdateMetadataParams, error) {
	const reason = "vector update metadata"
	name, off, err := readVectorName(payload, 0, reason)
	if err != nil {
		return VectorUpdateMetadataParams{}, err
	}
	if off+16 > len(payload) {
		return VectorUpdateMetadataParams{}, &InvalidCommandError{Reason: reason + ": truncated id/timestamp"}
	}
	id := binary.LittleEndian.Uint64(payload[off:])
	off += 8
	timestamp := int64(binary.LittleEndian.Uint64(payload[off:]))
	off += 8
	metadata, _, err := ReadParamBlock(payload, off, reason)
	if err != nil {
		return VectorUpdateMetadataParams{}, err
	}
	return VectorUpdateMetadataParams{Name: name, ID: id, TimestampUnixNano: timestamp, Metadata: metadata}, nil
}

// EncodeVectorIndexReconfigure creates a binary OpVectorIndexReconfigure
// command.
func EncodeVectorIndexReconfigure(p VectorIndexReconfigureParams) ([]byte, error) {
	nameSize, err := vectorNameSize(p.Name)
	if err != nil {
		return nil, err
	}
	paramSize, err := ParamBlockSize(p.IndexParams)
	if err != nil {
		return nil, err
	}
	size, err := addSizes(headerSize, nameSize, 1, paramSize)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, size)
	off := 0
	buf[off] = OpVectorIndexReconfigure
	off++
	if off, err = writeVectorName(buf, off, p.Name); err != nil {
		return nil, err
	}
	buf[off] = p.IndexType
	off++
	if _, err = WriteParamBlock(buf, off, p.IndexParams); err != nil {
		return nil, err
	}
	return buf, nil
}

// DecodeVectorIndexReconfigure decodes an OpVectorIndexReconfigure payload.
func DecodeVectorIndexReconfigure(payload []byte) (VectorIndexReconfigureParams, error) {
	const reason = "vector index reconfigure"
	name, off, err := readVectorName(payload, 0, reason)
	if err != nil {
		return VectorIndexReconfigureParams{}, err
	}
	if off+1 > len(payload) {
		return VectorIndexReconfigureParams{}, &InvalidCommandError{Reason: reason + ": truncated index type"}
	}
	indexType := payload[off]
	off++
	params, _, err := ReadParamBlock(payload, off, reason)
	if err != nil {
		return VectorIndexReconfigureParams{}, err
	}
	return VectorIndexReconfigureParams{Name: name, IndexType: indexType, IndexParams: params}, nil
}
