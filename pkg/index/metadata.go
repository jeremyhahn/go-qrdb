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

import (
	"math"
	"reflect"
	"strconv"
	"strings"
	"unsafe"

	"github.com/jeremyhahn/go-qrdb/pkg/errors"
)

const (
	// IndexTagName is the struct tag key for index configuration.
	IndexTagName = "index"
)

// ParseIndexTags extracts index metadata from struct type tags.
// It returns a map of field names to their index metadata.
// Supports tag formats:
//   - `index:"true"` - creates a normal non-unique index
//   - `index:"unique"` - creates a unique index
//   - `index:"case_insensitive"` - creates a case-insensitive index for strings
//   - `index:"unique,case_insensitive"` - combines multiple options
func ParseIndexTags(structType reflect.Type) (map[string]*IndexMetadata, error) {
	if structType == nil {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.ParseIndexTags",
			"structType cannot be nil",
		)
	}

	// Dereference pointer if needed
	if structType.Kind() == reflect.Pointer {
		structType = structType.Elem()
	}

	if structType.Kind() != reflect.Struct {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.ParseIndexTags",
			"expected struct type, got %s",
			structType.Kind(),
		)
	}

	metadata := make(map[string]*IndexMetadata)

	// Iterate through all fields
	for i := 0; i < structType.NumField(); i++ {
		field := structType.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		// Check for index tag
		tag := field.Tag.Get(IndexTagName)
		if tag == "" {
			continue
		}

		// Parse tag options
		indexMeta, err := parseIndexTag(field.Name, field.Type, tag)
		if err != nil {
			return nil, errors.Wrapf(
				err,
				errors.ErrInvalidInput,
				"index.ParseIndexTags",
				"failed to parse index tag for field %s",
				field.Name,
			)
		}

		metadata[field.Name] = indexMeta
	}

	return metadata, nil
}

// parseIndexTag parses a single index tag value into metadata.
func parseIndexTag(fieldName string, fieldType reflect.Type, tag string) (*IndexMetadata, error) {
	if tag == "" {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.parseIndexTag",
			"tag cannot be empty",
		)
	}

	// Initialize with defaults
	config := IndexConfig{
		Name:          fieldName,
		Type:          IndexTypeNormal,
		CaseSensitive: true, // default to case-sensitive
	}

	// Parse comma-separated options
	options := strings.Split(tag, ",")
	for _, opt := range options {
		opt = strings.TrimSpace(opt)
		opt = strings.ToLower(opt)

		switch opt {
		case "true", "normal":
			config.Type = IndexTypeNormal
		case "unique":
			config.Type = IndexTypeUnique
		case "case_insensitive", "ci":
			config.CaseSensitive = false
		case "case_sensitive", "cs":
			config.CaseSensitive = true
		case "sparse":
			config.Sparse = true
		case "fulltext", "fts":
			// Full-text search tag -- handled separately by the FTS system.
			// Treated as a normal index for tag parsing purposes.
			config.Type = IndexTypeNormal
		default:
			return nil, errors.Newf(
				errors.ErrInvalidInput,
				"index.parseIndexTag",
				"unknown index tag option: %s",
				opt,
			)
		}
	}

	meta := &IndexMetadata{
		FieldName: fieldName,
		FieldType: fieldType.String(),
		Config:    config,
		Tag:       tag,
	}

	return meta, nil
}

// GetFieldValue extracts the value of a field from a struct using reflection.
// Returns the field value as bytes suitable for indexing.
func GetFieldValue(entity interface{}, fieldName string) ([]byte, error) {
	if entity == nil {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.GetFieldValue",
			"entity cannot be nil",
		)
	}

	val := reflect.ValueOf(entity)

	// Dereference pointer if needed
	if val.Kind() == reflect.Pointer {
		if val.IsNil() {
			return nil, errors.Newf(
				errors.ErrInvalidInput,
				"index.GetFieldValue",
				"entity pointer cannot be nil",
			)
		}
		val = val.Elem()
	}

	if val.Kind() != reflect.Struct {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.GetFieldValue",
			"expected struct, got %s",
			val.Kind(),
		)
	}

	// Get the field
	field := val.FieldByName(fieldName)
	if !field.IsValid() {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.GetFieldValue",
			"field %s not found",
			fieldName,
		)
	}

	// Convert field value to bytes
	return fieldValueToBytes(field)
}

// fieldValueToBytes converts a reflect.Value to bytes for indexing.
func fieldValueToBytes(val reflect.Value) ([]byte, error) {
	if !val.IsValid() {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.fieldValueToBytes",
			"invalid value",
		)
	}

	// Handle different types
	switch val.Kind() {
	case reflect.String:
		return []byte(val.String()), nil

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return encodeInt64(val.Int()), nil

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return encodeUint64(val.Uint()), nil

	case reflect.Bool:
		if val.Bool() {
			return []byte{1}, nil
		}
		return []byte{0}, nil

	case reflect.Float32, reflect.Float64:
		return encodeFloat64(val.Float()), nil

	case reflect.Slice:
		// Handle []byte specially
		if val.Type().Elem().Kind() == reflect.Uint8 {
			return val.Bytes(), nil
		}
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.fieldValueToBytes",
			"unsupported slice type: %s",
			val.Type(),
		)

	default:
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.fieldValueToBytes",
			"unsupported field type: %s",
			val.Kind(),
		)
	}
}

// encodeUint64 encodes a uint64 to bytes in big-endian order for proper sorting.
func encodeUint64(v uint64) []byte {
	b := make([]byte, 8)
	b[0] = byte(v >> 56)
	b[1] = byte(v >> 48)
	b[2] = byte(v >> 40)
	b[3] = byte(v >> 32)
	b[4] = byte(v >> 24)
	b[5] = byte(v >> 16)
	b[6] = byte(v >> 8)
	b[7] = byte(v)
	return b
}

// encodeInt64 encodes an int64 to bytes with proper sorting (handles negative numbers).
func encodeInt64(v int64) []byte {
	// Flip the sign bit for proper sorting
	u := uint64(v) ^ (1 << 63) //nolint:gosec // G115: int64 to uint64 for proper sorting
	return encodeUint64(u)
}

// encodeFloat64 encodes a float64 to bytes with proper sorting.
func encodeFloat64(v float64) []byte {
	//nolint:gosec // G103: unsafe used for zero-copy float64 to uint64 conversion - performance critical
	// Convert to uint64 bits
	bits := *(*uint64)(unsafe.Pointer(&v))

	// Flip bits for negative numbers to ensure proper sorting
	if v < 0 {
		bits = ^bits
	} else {
		bits ^= (1 << 63)
	}

	return encodeUint64(bits)
}

// decodeUint64 decodes bytes to uint64.
func decodeUint64(b []byte) uint64 {
	if len(b) != 8 {
		return 0
	}
	return uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 |
		uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7])
}

// GetIndexableFields returns a list of field names that should be indexed.
func GetIndexableFields(structType reflect.Type) ([]string, error) {
	metadata, err := ParseIndexTags(structType)
	if err != nil {
		return nil, err
	}

	fields := make([]string, 0, len(metadata))
	for fieldName := range metadata {
		fields = append(fields, fieldName)
	}

	return fields, nil
}

// NormalizeFieldValue normalizes a field value based on index configuration.
// For numeric indexes it encodes the value as a fixed-width big-endian int64
// with sign-bit flip so that byte ordering matches numeric ordering.
// For case-insensitive string indexes it converts the value to lowercase.
// Returns nil and false when a numeric value cannot be parsed; callers should
// skip the index entry in that case.
func NormalizeFieldValue(value []byte, config IndexConfig) ([]byte, bool) {
	if config.ValueType == IndexValueTypeNumeric {
		encoded, err := EncodeNumericValue(string(value))
		if err != nil {
			return nil, false
		}
		return encoded, true
	}
	if !config.CaseSensitive && len(value) > 0 {
		return []byte(strings.ToLower(string(value))), true
	}
	return value, true
}

// DecodeFieldValue reverses the encoding applied by NormalizeFieldValue.
// For numeric indexes it decodes the 8-byte big-endian representation back
// to a decimal string. For string indexes it returns the raw bytes as a string.
func DecodeFieldValue(encoded []byte, config IndexConfig) string {
	if config.ValueType == IndexValueTypeNumeric {
		s, err := DecodeNumericValue(encoded)
		if err != nil {
			return string(encoded)
		}
		return s
	}
	return string(encoded)
}

// EncodeNumericValue parses s as a float64 (to handle both integer and
// floating-point JSON numbers) and encodes the result as an 8-byte
// big-endian value with a sign-bit flip so that byte order matches
// numeric order.
func EncodeNumericValue(s string) ([]byte, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.EncodeNumericValue",
			"cannot parse %q as a number: %v",
			s,
			err,
		)
	}
	return encodeFloat64(f), nil
}

// DecodeNumericValue reverses EncodeNumericValue. It decodes an 8-byte
// big-endian representation back to a decimal string. Integer values
// are returned without a decimal point (e.g. "123"); fractional values
// use the shortest representation that round-trips (e.g. "1.5").
func DecodeNumericValue(b []byte) (string, error) {
	if len(b) != 8 {
		return "", errors.Newf(
			errors.ErrInvalidInput,
			"index.DecodeNumericValue",
			"expected 8 bytes, got %d",
			len(b),
		)
	}
	bits := decodeUint64(b)

	// Reverse the encoding applied by encodeFloat64:
	// positive numbers had their sign bit flipped; negative numbers had
	// all bits flipped.
	var f float64
	if bits&(1<<63) != 0 {
		// Was positive — reverse the sign-bit flip.
		bits ^= (1 << 63)
	} else {
		// Was negative — reverse the all-bits flip.
		bits = ^bits
	}
	//nolint:gosec // G103: unsafe used for zero-copy uint64 to float64 conversion
	f = *(*float64)(unsafe.Pointer(&bits))

	// Format as integer when the value is whole and fits in int64.
	if f == math.Trunc(f) && f >= math.MinInt64 && f <= math.MaxInt64 {
		return strconv.FormatInt(int64(f), 10), nil
	}
	return strconv.FormatFloat(f, 'f', -1, 64), nil
}
