// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package dao

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strconv"
)

// DynamicEntity is an entity type that stores arbitrary JSON documents
// as a map of fields. It implements the Entity interface so it can be
// used with GenericDAO for collection-based document storage.
//
// Documents are serialized as flat JSON objects with the "id" field
// stored as a string to preserve uint64 precision:
//
//	{"id": "123", "name": "Alice", "email": "alice@example.com"}
type DynamicEntity struct {
	ID     uint64                 `json:"-"`
	Fields map[string]interface{} `json:"-"`
}

// MarshalJSON serializes the entity as a flat JSON object with the "id"
// field stored as a string to preserve uint64 precision.
func (d *DynamicEntity) MarshalJSON() ([]byte, error) {
	m := make(map[string]interface{}, len(d.Fields)+1)
	m["id"] = strconv.FormatUint(d.ID, 10)
	for k, v := range d.Fields {
		if k != "id" {
			m[k] = v
		}
	}
	return json.Marshal(m)
}

// UnmarshalJSON deserializes a flat JSON object, extracting the "id"
// field and placing all remaining fields into the Fields map. It also
// handles the legacy format where fields were wrapped in a "fields" key.
func (d *DynamicEntity) UnmarshalJSON(data []byte) error {
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}

	// Handle legacy format: {"id": N, "fields": {...}}
	if fieldsVal, ok := m["fields"]; ok {
		if fieldsMap, isMap := fieldsVal.(map[string]interface{}); isMap && len(m) <= 2 {
			d.Fields = fieldsMap
			if idVal, hasID := m["id"]; hasID {
				d.ID = jsonNumberToUint64(idVal)
			}
			return nil
		}
	}

	// Flat format: {"id": N, "name": "Alice", ...}
	if idVal, ok := m["id"]; ok {
		d.ID = jsonNumberToUint64(idVal)
		delete(m, "id")
	}
	d.Fields = m
	return nil
}

// jsonNumberToUint64 converts a JSON value to uint64. It handles
// string-encoded IDs (preferred for precision), float64 (from standard
// json.Unmarshal), and json.Number types.
func jsonNumberToUint64(v interface{}) uint64 {
	switch n := v.(type) {
	case string:
		if i, err := strconv.ParseUint(n, 10, 64); err == nil {
			return i
		}
	case float64:
		return uint64(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return uint64(i)
		}
	}
	return 0
}

// Compile-time interface check.
var _ Entity = (*DynamicEntity)(nil)

// EntityID returns the unique identifier for this entity.
func (d *DynamicEntity) EntityID() uint64 {
	return d.ID
}

// SetEntityID sets the entity's unique identifier.
func (d *DynamicEntity) SetEntityID(id uint64) {
	d.ID = id
}

// DynamicFieldHashGenerator generates deterministic IDs by hashing a named
// field from a DynamicEntity's Fields map using FNV-1a 64-bit. Unlike
// FieldHashGenerator, which uses struct reflection, this generator extracts
// values from the DynamicEntity.Fields map by key name.
type DynamicFieldHashGenerator struct {
	fieldName string
}

// NewDynamicFieldHashGenerator creates a DynamicFieldHashGenerator that
// hashes the value of the named field from DynamicEntity.Fields to produce
// entity IDs.
func NewDynamicFieldHashGenerator(fieldName string) *DynamicFieldHashGenerator {
	return &DynamicFieldHashGenerator{fieldName: fieldName}
}

// NextID extracts the named field from the DynamicEntity's Fields map
// and returns its FNV-1a 64-bit hash. Returns 0 if the entity is not a
// DynamicEntity or the field is not found.
func (g *DynamicFieldHashGenerator) NextID(entity Entity) uint64 {
	de, ok := entity.(*DynamicEntity)
	if !ok {
		return 0
	}
	if de.Fields == nil {
		return 0
	}
	val, exists := de.Fields[g.fieldName]
	if !exists {
		return 0
	}

	h := fnv.New64a()
	_, _ = fmt.Fprint(h, val)
	return h.Sum64()
}

// FieldName returns the field name used for hashing.
func (g *DynamicFieldHashGenerator) FieldName() string {
	return g.fieldName
}

// CryptoDynamicFieldHashGenerator is DynamicFieldHashGenerator's
// collision-resistant counterpart, hashing the named Fields map entry with
// SHA-256 folded to 64 bits (foldSHA256ToUint64) instead of FNV-1a. See
// CryptoFieldHashGenerator's doc for what this does and does not add over
// the FNV-1a generator: the same 64-bit ID space and birthday bound, but a
// preimage attack is required to engineer a chosen collision rather than
// none at all. DynamicFieldHashGenerator itself is unchanged for the same
// reason FieldHashGenerator is: changing its hash in place would silently
// orphan every entity already persisted under it (go-qrdb#589).
type CryptoDynamicFieldHashGenerator struct {
	fieldName string
}

// NewCryptoDynamicFieldHashGenerator creates a CryptoDynamicFieldHashGenerator
// that hashes the value of the named field from DynamicEntity.Fields to
// produce entity IDs.
func NewCryptoDynamicFieldHashGenerator(fieldName string) *CryptoDynamicFieldHashGenerator {
	return &CryptoDynamicFieldHashGenerator{fieldName: fieldName}
}

// NextID extracts the named field from the DynamicEntity's Fields map and
// returns the SHA-256 digest of its string form, folded to 64 bits. Returns
// 0 if the entity is not a DynamicEntity or the field is not found,
// matching DynamicFieldHashGenerator's contract.
func (g *CryptoDynamicFieldHashGenerator) NextID(entity Entity) uint64 {
	de, ok := entity.(*DynamicEntity)
	if !ok || de.Fields == nil {
		return 0
	}
	val, exists := de.Fields[g.fieldName]
	if !exists {
		return 0
	}
	sum := sha256.Sum256([]byte(fmt.Sprint(val)))
	return foldSHA256ToUint64(sum)
}

// FieldName returns the field name used for hashing.
func (g *CryptoDynamicFieldHashGenerator) FieldName() string {
	return g.fieldName
}
