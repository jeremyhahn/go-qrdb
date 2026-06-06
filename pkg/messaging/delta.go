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

// Package messaging provides delta computation for efficient change notifications.
// It computes the differences between two entity states and produces a DeltaPayload
// containing only the changed fields, reducing bandwidth for watch events.
package messaging

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
)

// Default configuration values for DeltaComputer.
const (
	defaultDeltaMaxDepth = 10
)

// Diffable is an optional interface entities can implement for optimized delta detection.
type Diffable interface {
	// Diff computes the differences between this entity and the previous state.
	// Returns a list of field changes representing the delta.
	Diff(previous interface{}) ([]FieldChange, error)

	// Apply applies the given field changes to this entity.
	Apply(changes []FieldChange) error

	// Version returns the version number of this entity.
	Version() uint64
}

// DeltaComputer computes deltas between entity states.
// It uses reflection to compare struct fields recursively and supports
// the Diffable interface for optimized comparison when available.
type DeltaComputer struct {
	maxDepth   int
	ignoreZero bool
	logger     *logger.Logger
}

// DeltaComputerOption configures DeltaComputer.
type DeltaComputerOption func(*DeltaComputer)

// WithDeltaMaxDepth sets the maximum recursion depth for nested struct comparison.
func WithDeltaMaxDepth(depth int) DeltaComputerOption {
	return func(dc *DeltaComputer) {
		if depth > 0 {
			dc.maxDepth = depth
		}
	}
}

// WithDeltaIgnoreZeroChanges configures whether to ignore zero-value to zero-value changes.
func WithDeltaIgnoreZeroChanges(ignore bool) DeltaComputerOption {
	return func(dc *DeltaComputer) {
		dc.ignoreZero = ignore
	}
}

// NewDeltaComputer creates a new DeltaComputer with the given options.
func NewDeltaComputer(log *logger.Logger, opts ...DeltaComputerOption) *DeltaComputer {
	dc := &DeltaComputer{
		maxDepth:   defaultDeltaMaxDepth,
		ignoreZero: true,
		logger:     log,
	}

	for _, opt := range opts {
		opt(dc)
	}

	return dc
}

// Compute computes the delta between two entity states.
// Both old and new must be of the same type (or old can be nil for new entity creation).
// Returns a DeltaPayload containing the list of changes.
func (dc *DeltaComputer) Compute(old, new interface{}) (*DeltaPayload, error) {
	if new == nil {
		return nil, &DeltaNilInputError{Parameter: "new entity"}
	}

	// Check if new implements Diffable interface for optimized comparison
	if diffable, ok := new.(Diffable); ok {
		return dc.computeWithDiffable(old, diffable)
	}

	newVal := reflect.ValueOf(new)
	if newVal.Kind() == reflect.Pointer {
		if newVal.IsNil() {
			return nil, &DeltaNilInputError{Parameter: "new entity (nil pointer)"}
		}
		newVal = newVal.Elem()
	}

	var oldVal reflect.Value
	if old != nil {
		oldVal = reflect.ValueOf(old)
		if oldVal.Kind() == reflect.Pointer {
			if oldVal.IsNil() {
				old = nil
			} else {
				oldVal = oldVal.Elem()
			}
		}
	}

	// Handle nil old value (new entity creation)
	if old == nil {
		return dc.computeNewEntity(newVal)
	}

	// Verify types match
	if oldVal.Type() != newVal.Type() {
		return nil, &DeltaTypeMismatchError{
			OldType: oldVal.Type().String(),
			NewType: newVal.Type().String(),
		}
	}

	// Compute changes recursively
	changes, err := dc.computeChanges(oldVal, newVal, "", 0)
	if err != nil {
		return nil, err
	}

	// Extract version information if available
	var version, prevVersion uint64
	if v, ok := new.(interface{ Version() uint64 }); ok {
		version = v.Version()
	}
	if v, ok := old.(interface{ Version() uint64 }); ok {
		prevVersion = v.Version()
	}

	// Extract entity ID and type if available
	entityID := dc.extractEntityID(new)
	entityType := dc.extractEntityType(newVal.Type())

	return &DeltaPayload{
		EntityID:    entityID,
		EntityType:  entityType,
		Version:     version,
		PrevVersion: prevVersion,
		Timestamp:   time.Now(),
		Changes:     changes,
	}, nil
}

// ComputeBytes computes the delta from serialized data.
// The entityType parameter should be a pointer to the struct type to unmarshal into.
func (dc *DeltaComputer) ComputeBytes(oldData, newData []byte, entityType interface{}) (*DeltaPayload, error) {
	if len(newData) == 0 {
		return nil, &DeltaNilInputError{Parameter: "new data"}
	}

	if entityType == nil {
		return nil, &DeltaNilInputError{Parameter: "entity type"}
	}

	// Get the type to create new instances
	entityVal := reflect.ValueOf(entityType)
	if entityVal.Kind() != reflect.Pointer {
		return nil, &DeltaTypeMismatchError{
			OldType: "non-pointer",
			NewType: "pointer",
			Path:    "entityType",
		}
	}

	elemType := entityVal.Type().Elem()

	// Unmarshal new data
	newPtr := reflect.New(elemType)
	if err := json.Unmarshal(newData, newPtr.Interface()); err != nil {
		return nil, &DeltaDeserializationError{Cause: "new data: " + err.Error()}
	}

	// Handle old data
	var old interface{}
	if len(oldData) > 0 {
		oldPtr := reflect.New(elemType)
		if err := json.Unmarshal(oldData, oldPtr.Interface()); err != nil {
			return nil, &DeltaDeserializationError{Cause: "old data: " + err.Error()}
		}
		old = oldPtr.Interface()
	}

	return dc.Compute(old, newPtr.Interface())
}

// computeWithDiffable uses the Diffable interface for optimized comparison.
func (dc *DeltaComputer) computeWithDiffable(old interface{}, diffable Diffable) (*DeltaPayload, error) {
	changes, err := diffable.Diff(old)
	if err != nil {
		return nil, &DeltaDiffError{Cause: err.Error()}
	}

	version := diffable.Version()
	var prevVersion uint64
	if oldDiffable, ok := old.(Diffable); ok {
		prevVersion = oldDiffable.Version()
	}

	newVal := reflect.ValueOf(diffable)
	if newVal.Kind() == reflect.Pointer {
		newVal = newVal.Elem()
	}

	return &DeltaPayload{
		EntityID:    dc.extractEntityID(diffable),
		EntityType:  dc.extractEntityType(newVal.Type()),
		Version:     version,
		PrevVersion: prevVersion,
		Timestamp:   time.Now(),
		Changes:     changes,
	}, nil
}

// computeNewEntity creates a delta payload for a new entity (all fields are set operations).
func (dc *DeltaComputer) computeNewEntity(newVal reflect.Value) (*DeltaPayload, error) {
	changes, err := dc.extractAllFields(newVal, "", 0)
	if err != nil {
		return nil, err
	}

	var version uint64
	if newVal.CanAddr() {
		if v, ok := newVal.Addr().Interface().(interface{ Version() uint64 }); ok {
			version = v.Version()
		}
	}

	return &DeltaPayload{
		EntityID:    dc.extractEntityIDFromValue(newVal),
		EntityType:  dc.extractEntityType(newVal.Type()),
		Version:     version,
		PrevVersion: 0,
		Timestamp:   time.Now(),
		Changes:     changes,
	}, nil
}

// extractAllFields extracts all non-zero fields as set operations.
func (dc *DeltaComputer) extractAllFields(val reflect.Value, path string, depth int) ([]FieldChange, error) {
	if depth > dc.maxDepth {
		return nil, &DeltaMaxDepthExceededError{MaxDepth: dc.maxDepth, Path: path}
	}

	if val.Kind() == reflect.Pointer {
		if val.IsNil() {
			return nil, nil
		}
		val = val.Elem()
	}

	var changes []FieldChange

	switch val.Kind() {
	case reflect.Struct:
		for i := 0; i < val.NumField(); i++ {
			field := val.Type().Field(i)
			fieldVal := val.Field(i)

			// Skip unexported fields
			if !field.IsExported() {
				continue
			}

			fieldPath := dc.getFieldPath(field)
			if fieldPath == "-" {
				continue
			}

			fullPath := dc.buildPath(path, fieldPath)

			// Handle nested structs
			if fieldVal.Kind() == reflect.Struct && !dc.isSpecialType(fieldVal.Type()) {
				nestedChanges, err := dc.extractAllFields(fieldVal, fullPath, depth+1)
				if err != nil {
					return nil, err
				}
				changes = append(changes, nestedChanges...)
				continue
			}

			// Handle pointer to struct
			if fieldVal.Kind() == reflect.Pointer && !fieldVal.IsNil() {
				elem := fieldVal.Elem()
				if elem.Kind() == reflect.Struct && !dc.isSpecialType(elem.Type()) {
					nestedChanges, err := dc.extractAllFields(elem, fullPath, depth+1)
					if err != nil {
						return nil, err
					}
					changes = append(changes, nestedChanges...)
					continue
				}
			}

			// Skip zero values if configured
			if dc.ignoreZero && dc.isZeroValue(fieldVal) {
				continue
			}

			changes = append(changes, FieldChange{
				Path:      fullPath,
				Operation: OpSet,
				Value:     dc.extractValue(fieldVal),
			})
		}

	case reflect.Map:
		iter := val.MapRange()
		for iter.Next() {
			key := iter.Key()
			mapVal := iter.Value()
			keyStr := dc.valueToString(key)
			fullPath := dc.buildPath(path, keyStr)

			if mapVal.Kind() == reflect.Struct && !dc.isSpecialType(mapVal.Type()) {
				nestedChanges, err := dc.extractAllFields(mapVal, fullPath, depth+1)
				if err != nil {
					return nil, err
				}
				changes = append(changes, nestedChanges...)
			} else {
				changes = append(changes, FieldChange{
					Path:      fullPath,
					Operation: OpSet,
					Value:     dc.extractValue(mapVal),
				})
			}
		}

	case reflect.Slice, reflect.Array:
		if !dc.isZeroValue(val) {
			changes = append(changes, FieldChange{
				Path:      path,
				Operation: OpSet,
				Value:     dc.extractValue(val),
			})
		}

	default:
		if !dc.isZeroValue(val) {
			changes = append(changes, FieldChange{
				Path:      path,
				Operation: OpSet,
				Value:     dc.extractValue(val),
			})
		}
	}

	return changes, nil
}

// computeChanges recursively compares two values and returns field changes.
func (dc *DeltaComputer) computeChanges(oldVal, newVal reflect.Value, path string, depth int) ([]FieldChange, error) {
	if depth > dc.maxDepth {
		return nil, &DeltaMaxDepthExceededError{MaxDepth: dc.maxDepth, Path: path}
	}

	// Handle pointers
	if oldVal.Kind() == reflect.Pointer && newVal.Kind() == reflect.Pointer {
		if oldVal.IsNil() && newVal.IsNil() {
			return nil, nil
		}
		if oldVal.IsNil() {
			return dc.extractAllFields(newVal.Elem(), path, depth)
		}
		if newVal.IsNil() {
			return []FieldChange{{
				Path:      path,
				Operation: OpDelete,
				OldValue:  dc.extractValue(oldVal.Elem()),
			}}, nil
		}
		return dc.computeChanges(oldVal.Elem(), newVal.Elem(), path, depth)
	}

	// Handle interface values
	if oldVal.Kind() == reflect.Interface && newVal.Kind() == reflect.Interface {
		if oldVal.IsNil() && newVal.IsNil() {
			return nil, nil
		}
		if oldVal.IsNil() {
			return []FieldChange{{
				Path:      path,
				Operation: OpSet,
				Value:     dc.extractValue(newVal),
			}}, nil
		}
		if newVal.IsNil() {
			return []FieldChange{{
				Path:      path,
				Operation: OpDelete,
				OldValue:  dc.extractValue(oldVal),
			}}, nil
		}
		return dc.computeChanges(oldVal.Elem(), newVal.Elem(), path, depth)
	}

	var changes []FieldChange

	switch oldVal.Kind() {
	case reflect.Struct:
		structChanges, err := dc.compareStructs(oldVal, newVal, path, depth)
		if err != nil {
			return nil, err
		}
		return structChanges, nil

	case reflect.Map:
		return dc.compareMaps(oldVal, newVal, path, depth)

	case reflect.Slice, reflect.Array:
		return dc.compareSlices(oldVal, newVal, path, depth)

	default:
		// Compare primitive values
		if !reflect.DeepEqual(oldVal.Interface(), newVal.Interface()) {
			oldZero := dc.isZeroValue(oldVal)
			newZero := dc.isZeroValue(newVal)

			// Skip zero-to-zero changes if configured
			if dc.ignoreZero && oldZero && newZero {
				return nil, nil
			}

			change := FieldChange{
				Path:      path,
				Operation: OpSet,
				Value:     dc.extractValue(newVal),
				OldValue:  dc.extractValue(oldVal),
			}

			// If new value is zero, treat as delete
			if newZero && !oldZero {
				change.Operation = OpDelete
				change.Value = nil
			}

			changes = append(changes, change)
		}
	}

	return changes, nil
}

// compareStructs compares two struct values field by field.
func (dc *DeltaComputer) compareStructs(oldVal, newVal reflect.Value, path string, depth int) ([]FieldChange, error) {
	var changes []FieldChange

	for i := 0; i < oldVal.NumField(); i++ {
		field := oldVal.Type().Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		fieldPath := dc.getFieldPath(field)
		if fieldPath == "-" {
			continue
		}

		fullPath := dc.buildPath(path, fieldPath)

		oldField := oldVal.Field(i)
		newField := newVal.Field(i)

		// Handle special types (time.Time, etc.) as primitives
		if dc.isSpecialType(field.Type) {
			if !reflect.DeepEqual(oldField.Interface(), newField.Interface()) {
				oldZero := dc.isZeroValue(oldField)
				newZero := dc.isZeroValue(newField)

				if dc.ignoreZero && oldZero && newZero {
					continue
				}

				change := FieldChange{
					Path:      fullPath,
					Operation: OpSet,
					Value:     dc.extractValue(newField),
					OldValue:  dc.extractValue(oldField),
				}

				if newZero && !oldZero {
					change.Operation = OpDelete
					change.Value = nil
				}

				changes = append(changes, change)
			}
			continue
		}

		// Handle nested structs and other types recursively
		fieldChanges, err := dc.computeChanges(oldField, newField, fullPath, depth+1)
		if err != nil {
			return nil, err
		}
		changes = append(changes, fieldChanges...)
	}

	return changes, nil
}

// compareMaps compares two map values.
func (dc *DeltaComputer) compareMaps(oldVal, newVal reflect.Value, path string, depth int) ([]FieldChange, error) {
	var changes []FieldChange

	// Track keys in old map
	oldKeys := make(map[interface{}]bool)
	if !oldVal.IsNil() {
		iter := oldVal.MapRange()
		for iter.Next() {
			key := iter.Key().Interface()
			oldKeys[key] = true
		}
	}

	// Process new map keys
	if !newVal.IsNil() {
		iter := newVal.MapRange()
		for iter.Next() {
			key := iter.Key()
			keyStr := dc.valueToString(key)
			fullPath := dc.buildPath(path, keyStr)

			newMapVal := iter.Value()
			oldMapVal := reflect.Value{}

			if !oldVal.IsNil() {
				oldMapVal = oldVal.MapIndex(key)
			}

			if !oldMapVal.IsValid() {
				// New key added
				if newMapVal.Kind() == reflect.Struct && !dc.isSpecialType(newMapVal.Type()) {
					nestedChanges, err := dc.extractAllFields(newMapVal, fullPath, depth+1)
					if err != nil {
						return nil, err
					}
					changes = append(changes, nestedChanges...)
				} else {
					changes = append(changes, FieldChange{
						Path:      fullPath,
						Operation: OpSet,
						Value:     dc.extractValue(newMapVal),
					})
				}
			} else {
				// Key exists in both, compare values
				delete(oldKeys, key.Interface())

				fieldChanges, err := dc.computeChanges(oldMapVal, newMapVal, fullPath, depth+1)
				if err != nil {
					return nil, err
				}
				changes = append(changes, fieldChanges...)
			}
		}
	}

	// Process deleted keys
	if !oldVal.IsNil() {
		iter := oldVal.MapRange()
		for iter.Next() {
			key := iter.Key()
			if oldKeys[key.Interface()] {
				keyStr := dc.valueToString(key)
				fullPath := dc.buildPath(path, keyStr)
				changes = append(changes, FieldChange{
					Path:      fullPath,
					Operation: OpDelete,
					OldValue:  dc.extractValue(iter.Value()),
				})
			}
		}
	}

	return changes, nil
}

// compareSlices compares two slice values.
func (dc *DeltaComputer) compareSlices(oldVal, newVal reflect.Value, path string, _ int) ([]FieldChange, error) {
	if !reflect.DeepEqual(oldVal.Interface(), newVal.Interface()) {
		oldZero := dc.isZeroValue(oldVal)
		newZero := dc.isZeroValue(newVal)

		if dc.ignoreZero && oldZero && newZero {
			return nil, nil
		}

		change := FieldChange{
			Path:      path,
			Operation: OpSet,
			Value:     dc.extractValue(newVal),
			OldValue:  dc.extractValue(oldVal),
		}

		if newZero && !oldZero {
			change.Operation = OpDelete
			change.Value = nil
		}

		return []FieldChange{change}, nil
	}

	return nil, nil
}

// getFieldPath returns the JSON field name from struct tags or falls back to field name.
func (dc *DeltaComputer) getFieldPath(field reflect.StructField) string {
	jsonTag := field.Tag.Get("json")
	if jsonTag == "" {
		return field.Name
	}

	// Handle json tags like "field_name,omitempty"
	parts := strings.Split(jsonTag, ",")
	if parts[0] == "" {
		return field.Name
	}

	return parts[0]
}

// buildPath constructs a dot-separated path.
func (dc *DeltaComputer) buildPath(base, segment string) string {
	if base == "" {
		return segment
	}
	return base + "." + segment
}

// isZeroValue checks if a reflect.Value is the zero value for its type.
func (dc *DeltaComputer) isZeroValue(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}

	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func:
		return v.IsNil()
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if !dc.isZeroValue(v.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Struct:
		// Special handling for time.Time.
		if v.Type() == reflect.TypeOf(time.Time{}) {
			t, ok := v.Interface().(time.Time)
			return ok && t.IsZero()
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() && !dc.isZeroValue(v.Field(i)) {
				return false
			}
		}
		return true
	default:
		return v.IsZero()
	}
}

// isSpecialType checks if a type should be treated as a primitive (not recursed into).
func (dc *DeltaComputer) isSpecialType(t reflect.Type) bool {
	// time.Time should be compared as a whole, not field by field
	if t == reflect.TypeOf(time.Time{}) {
		return true
	}
	// Pointer to time.Time
	if t.Kind() == reflect.Pointer && t.Elem() == reflect.TypeOf(time.Time{}) {
		return true
	}
	return false
}

// extractValue converts a reflect.Value to an interface{} suitable for JSON serialization.
func (dc *DeltaComputer) extractValue(v reflect.Value) interface{} {
	if !v.IsValid() {
		return nil
	}

	// Handle pointers and interfaces
	if v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		return dc.extractValue(v.Elem())
	}

	return v.Interface()
}

// valueToString converts a reflect.Value to a string for use in paths.
func (dc *DeltaComputer) valueToString(v reflect.Value) string {
	if v.Kind() == reflect.String {
		return v.String()
	}
	return strings.Trim(strings.ReplaceAll(
		strings.ReplaceAll(
			reflect.ValueOf(v.Interface()).String(),
			"<", ""),
		">", ""), " ")
}

// extractEntityID attempts to extract an ID field from the entity.
func (dc *DeltaComputer) extractEntityID(entity interface{}) string {
	val := reflect.ValueOf(entity)
	if val.Kind() == reflect.Pointer {
		if val.IsNil() {
			return ""
		}
		val = val.Elem()
	}

	return dc.extractEntityIDFromValue(val)
}

// extractEntityIDFromValue extracts ID from a reflect.Value.
func (dc *DeltaComputer) extractEntityIDFromValue(val reflect.Value) string {
	if val.Kind() != reflect.Struct {
		return ""
	}

	// Try common ID field names
	idFieldNames := []string{"ID", "Id", "EntityID", "EntityId", "Key"}
	for _, name := range idFieldNames {
		field := val.FieldByName(name)
		if field.IsValid() && field.Kind() == reflect.String {
			return field.String()
		}
	}

	return ""
}

// extractEntityType returns the type name of the entity.
func (dc *DeltaComputer) extractEntityType(t reflect.Type) string {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Name()
}
