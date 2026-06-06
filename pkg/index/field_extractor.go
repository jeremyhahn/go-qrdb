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
	"reflect"

	"github.com/jeremyhahn/go-qrdb/pkg/errors"
)

// FieldExtractor is a closure that extracts a field value from an entity
// as bytes suitable for indexing. It uses a pre-computed field index to
// avoid the O(n) field name lookup that reflect.Value.FieldByName performs.
type FieldExtractor func(entity interface{}) ([]byte, error)

// FieldExtractorMap maps field names to their pre-computed FieldExtractor closures.
type FieldExtractorMap map[string]FieldExtractor

// NewFieldExtractor creates a FieldExtractor for the given struct field.
// The field index is captured at creation time, so subsequent calls use
// reflect.Value.FieldByIndex(index) which is O(1) instead of
// FieldByName(name) which is O(n) per struct field count.
func NewFieldExtractor(structType reflect.Type, fieldName string) (FieldExtractor, error) {
	if structType == nil {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.NewFieldExtractor",
			"structType cannot be nil",
		)
	}

	// Dereference pointer type if needed.
	if structType.Kind() == reflect.Pointer {
		structType = structType.Elem()
	}

	if structType.Kind() != reflect.Struct {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.NewFieldExtractor",
			"expected struct type, got %s",
			structType.Kind(),
		)
	}

	// Look up the field once at creation time.
	field, ok := structType.FieldByName(fieldName)
	if !ok {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.NewFieldExtractor",
			"field %s not found in %s",
			fieldName,
			structType.Name(),
		)
	}

	// Capture the pre-computed field index in the closure.
	fieldIndex := field.Index

	extractor := func(entity interface{}) ([]byte, error) {
		if entity == nil {
			return nil, errors.Newf(
				errors.ErrInvalidInput,
				"index.FieldExtractor",
				"entity cannot be nil",
			)
		}

		val := reflect.ValueOf(entity)

		// Dereference pointer if needed.
		if val.Kind() == reflect.Pointer {
			if val.IsNil() {
				return nil, errors.Newf(
					errors.ErrInvalidInput,
					"index.FieldExtractor",
					"entity pointer cannot be nil",
				)
			}
			val = val.Elem()
		}

		if val.Kind() != reflect.Struct {
			return nil, errors.Newf(
				errors.ErrInvalidInput,
				"index.FieldExtractor",
				"expected struct, got %s",
				val.Kind(),
			)
		}

		// O(1) field access using the pre-computed index.
		fieldVal := val.FieldByIndex(fieldIndex)

		return fieldValueToBytes(fieldVal)
	}

	return extractor, nil
}

// NewFieldExtractors creates FieldExtractors for all indexed fields
// parsed from the struct type's index tags. This is intended to be called
// once at RegisterIndexes time so the hot path avoids repeated tag parsing
// and field name lookups.
func NewFieldExtractors(structType reflect.Type) (FieldExtractorMap, error) {
	if structType == nil {
		return nil, errors.Newf(
			errors.ErrInvalidInput,
			"index.NewFieldExtractors",
			"structType cannot be nil",
		)
	}

	metadata, err := ParseIndexTags(structType)
	if err != nil {
		return nil, err
	}

	extractors := make(FieldExtractorMap, len(metadata))
	for fieldName := range metadata {
		extractor, extractorErr := NewFieldExtractor(structType, fieldName)
		if extractorErr != nil {
			return nil, extractorErr
		}
		extractors[fieldName] = extractor
	}

	return extractors, nil
}
