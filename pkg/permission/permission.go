// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package permission defines fine-grained access control permissions in
// "resource:action" format, a global registry for runtime validation, and
// category groupings for UI rendering.
package permission

import (
	"sort"
	"strings"
	"sync"
)

// Permission represents a fine-grained access control permission in
// "resource:action" format (e.g., "kv:put", "admin:health").
type Permission string

// String returns the permission as a plain string.
func (p Permission) String() string { return string(p) }

// Resource returns the resource component of p (the portion before the colon).
// If p contains no colon, the entire string is returned.
func Resource(p Permission) string {
	if i := strings.IndexByte(string(p), ':'); i >= 0 {
		return string(p)[:i]
	}
	return string(p)
}

// Action returns the action component of p (the portion after the colon).
// If p contains no colon, an empty string is returned.
func Action(p Permission) string {
	if i := strings.IndexByte(string(p), ':'); i >= 0 {
		return string(p)[i+1:]
	}
	return ""
}

// Category groups related permissions under a display name for UI rendering
// (e.g., a permission tree picker).
type Category struct {
	Name        string       // Display name (e.g., "KV Service").
	Group       string       // Logical grouping (e.g., "Data Services").
	Permissions []Permission // Permissions belonging to this category.
}

// ---------------------------------------------------------------------------
// Global registry
// ---------------------------------------------------------------------------

var (
	mu       sync.RWMutex
	registry = map[Permission]struct{}{}
)

// Register adds one or more permissions to the global registry. It is safe
// for concurrent use and is typically called from init() functions in
// feature-gated files.
func Register(perms ...Permission) {
	mu.Lock()
	defer mu.Unlock()
	for _, p := range perms {
		registry[p] = struct{}{}
	}
}

// All returns every registered permission as a sorted slice.
func All() []Permission {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Permission, 0, len(registry))
	for p := range registry {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i] < out[j]
	})
	return out
}

// IsRegistered reports whether p exists in the global registry.
func IsRegistered(p Permission) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := registry[p]
	return ok
}

// Parse validates a raw string and returns the corresponding Permission.
// It returns an InvalidPermissionError if the string does not contain
// exactly one colon separating a non-empty resource and a non-empty action.
func Parse(s string) (Permission, error) {
	i := strings.IndexByte(s, ':')
	if i < 0 {
		return "", &InvalidPermissionError{Value: s}
	}
	resource := s[:i]
	action := s[i+1:]
	if resource == "" || action == "" {
		return "", &InvalidPermissionError{Value: s}
	}
	return Permission(s), nil
}

// Categories returns all permission categories for UI rendering. The
// returned slice is ordered by logical grouping.
func Categories() []Category {
	return categories
}

// ByResource returns all registered permissions whose resource component
// matches the given name.
func ByResource(resource string) []Permission {
	mu.RLock()
	defer mu.RUnlock()
	var out []Permission
	for p := range registry {
		if Resource(p) == resource {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i] < out[j]
	})
	return out
}
