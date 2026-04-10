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

// Package entity provides a standalone, thread-safe entity type registry
// for the Unified Entity-to-SQL Bridge. It carries only stdlib dependencies
// and serves as the shared metadata foundation for both the DAO and SQL layers.
package entity

import (
	"reflect"
	"sort"
	"sync"
)

// FieldMeta describes a single struct field for SQL schema derivation.
type FieldMeta struct {
	// GoName is the Go struct field name, e.g. "Email".
	GoName string

	// JSONName is the JSON tag name, e.g. "email".
	JSONName string

	// GoType is the reflect.Type of the field.
	GoType reflect.Type

	// GoKind is the reflect.Kind of the field.
	GoKind reflect.Kind

	// IsPrimaryKey marks the field as the primary key.
	IsPrimaryKey bool

	// Indexed is true when the field carries index:"true" or index:"unique".
	Indexed bool

	// Unique is true when the field carries index:"unique".
	Unique bool

	// Sparse indicates a sparse index (skips zero values).
	Sparse bool

	// CaseSensitive controls case-sensitivity for string indexes.
	CaseSensitive bool
}

// Registration holds entity type metadata for both the DAO and SQL layers.
type Registration struct {
	// EntityType is the canonical name for this entity, e.g. "users".
	EntityType string

	// StructType is the reflect.Type of the entity struct, e.g. reflect.TypeOf(User{}).
	StructType reflect.Type

	// Fields is the ordered slice of exported field descriptors.
	Fields []FieldMeta

	// IDFieldJSON is the JSON name of the primary-key field, e.g. "id".
	IDFieldJSON string

	// IDFieldGo is the Go name of the primary-key field, e.g. "ID".
	IDFieldGo string

	// Database optionally scopes this registration to a single database.
	Database string

	// CodecType identifies the codec used by the DAO, e.g. "json", "msgpack".
	CodecType string

	// NewFunc is a type-erased factory that returns a new zero-value entity.
	NewFunc func() any

	// MarshalFunc serializes an entity to bytes using the same codec the DAO
	// uses. It accepts any and returns the encoded bytes.
	MarshalFunc func(v any) ([]byte, error)

	// UnmarshalFunc deserializes bytes into a new entity using the same codec
	// the DAO uses. It returns a pointer to the decoded entity.
	UnmarshalFunc func(data []byte) (any, error)
}

// Registry is a thread-safe store of entity type registrations.
type Registry struct {
	mu    sync.RWMutex
	types map[string]*Registration
}

// NewRegistry creates and returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{types: make(map[string]*Registration)}
}

// Register adds reg to the registry. Registration is idempotent when the same
// StructType is re-registered under the same EntityType. Registering a different
// StructType for an already-registered EntityType returns AlreadyRegisteredError.
// Missing required fields return InvalidRegistrationError.
func (r *Registry) Register(reg *Registration) error {
	if err := validateRegistration(reg); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.types[reg.EntityType]
	if ok {
		if existing.StructType == reg.StructType {
			// Idempotent: same type registered again — no-op.
			return nil
		}
		return &AlreadyRegisteredError{EntityType: reg.EntityType}
	}

	r.types[reg.EntityType] = reg
	return nil
}

// Get performs a thread-safe lookup by entity type name. It returns the
// Registration and true when found, or nil and false when not found.
func (r *Registry) Get(entityType string) (*Registration, bool) {
	r.mu.RLock()
	reg, ok := r.types[entityType]
	r.mu.RUnlock()
	return reg, ok
}

// List returns all registrations sorted by EntityType.
func (r *Registry) List() []*Registration {
	r.mu.RLock()
	out := make([]*Registration, 0, len(r.types))
	for _, reg := range r.types {
		out = append(out, reg)
	}
	r.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		return out[i].EntityType < out[j].EntityType
	})
	return out
}

// ListForDatabase returns all registrations whose Database field matches db,
// sorted by EntityType.
func (r *Registry) ListForDatabase(db string) []*Registration {
	r.mu.RLock()
	out := make([]*Registration, 0)
	for _, reg := range r.types {
		if reg.Database == db {
			out = append(out, reg)
		}
	}
	r.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		return out[i].EntityType < out[j].EntityType
	})
	return out
}

// Unregister removes the registration for entityType. It is a no-op when the
// entity type is not registered.
func (r *Registry) Unregister(entityType string) {
	r.mu.Lock()
	delete(r.types, entityType)
	r.mu.Unlock()
}

// validateRegistration checks that all required fields of reg are populated.
func validateRegistration(reg *Registration) error {
	if reg.EntityType == "" {
		return &InvalidRegistrationError{Field: "EntityType", Reason: "must not be empty"}
	}
	if reg.StructType == nil {
		return &InvalidRegistrationError{Field: "StructType", Reason: "must not be nil"}
	}
	if len(reg.Fields) == 0 {
		return &InvalidRegistrationError{Field: "Fields", Reason: "must not be empty"}
	}
	if reg.IDFieldJSON == "" {
		return &InvalidRegistrationError{Field: "IDFieldJSON", Reason: "must not be empty"}
	}
	if reg.IDFieldGo == "" {
		return &InvalidRegistrationError{Field: "IDFieldGo", Reason: "must not be empty"}
	}
	if reg.NewFunc == nil {
		return &InvalidRegistrationError{Field: "NewFunc", Reason: "must not be nil"}
	}
	if reg.MarshalFunc == nil {
		return &InvalidRegistrationError{Field: "MarshalFunc", Reason: "must not be nil"}
	}
	if reg.UnmarshalFunc == nil {
		return &InvalidRegistrationError{Field: "UnmarshalFunc", Reason: "must not be nil"}
	}
	return nil
}

// globalRegistry is the package-level singleton Registry.
var globalRegistry = &Registry{types: make(map[string]*Registration)}

// Global returns the package-level entity type registry.
func Global() *Registry { return globalRegistry }
