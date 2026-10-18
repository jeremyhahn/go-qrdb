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

// Unregistered returns the members of perms that the global registry does not
// define, in input order. Whole-segment wildcards are never reported: the RBAC
// authorizer expands them against concrete checks at authorization time, so
// there is no registry entry for one to lose.
//
// It exists rather than a loop over IsRegistered because the caller is on the
// authorization read path — every role read screens its whole permission list —
// and a role may hold the entire registry (Owner holds All()). One lock
// acquisition for the list, not one per permission.
func Unregistered(perms []Permission) []Permission {
	if len(perms) == 0 {
		return nil
	}
	mu.RLock()
	defer mu.RUnlock()
	var out []Permission
	for _, p := range perms {
		if IsWildcard(p) {
			continue
		}
		if _, ok := registry[p]; !ok {
			out = append(out, p)
		}
	}
	return out
}

// unregisterForTesting removes the given permissions from the registry. It
// exists so a test that registers a probe permission can leave the
// process-global registry as it found it: TestEveryPermissionDeclaresItsScope
// walks All(), and a probe left behind by an earlier test reads as an
// undeclared built-in. The function is unexported and not part of the
// public API; production code never unregisters a permission.
func unregisterForTesting(perms ...Permission) {
	mu.Lock()
	defer mu.Unlock()
	for _, p := range perms {
		delete(registry, p)
	}
}

// IsWildcard reports whether p uses a whole-segment "*" wildcard in its
// resource or action component (e.g. "keys:*", "*:read", "*:*"). Wildcard
// permissions are matched against concrete resource:action checks by the
// RBAC authorizer; they are accepted as valid role grants without being
// listed in the concrete permission registry.
func IsWildcard(p Permission) bool {
	return Resource(p) == "*" || Action(p) == "*"
}

// Expand returns the concrete permissions p stands for: p itself when p is
// concrete, and every registered permission it matches, sorted, when p is a
// whole-segment wildcard. A wildcard is a name-matcher over the registry, not
// a permission of its own -- it has no registry entry and no scope -- so a
// caller that needs per-permission facts about a grant (its scope, above all)
// asks about the members rather than the pattern. A wildcard that matches
// nothing expands to nil. The expansion is concrete by construction: a
// wildcard that something has put in the registry (Register does not refuse
// one) is a pattern, not a member, and is never returned.
func Expand(p Permission) []Permission {
	if !IsWildcard(p) {
		return []Permission{p}
	}
	resource, action := Resource(p), Action(p)
	mu.RLock()
	defer mu.RUnlock()
	var out []Permission
	for candidate := range registry {
		if IsWildcard(candidate) {
			continue
		}
		if (resource == "*" || Resource(candidate) == resource) &&
			(action == "*" || Action(candidate) == action) {
			out = append(out, candidate)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ExpandAll returns the concrete permissions perms cover: each one passed
// through Expand, deduplicated and sorted by name.
func ExpandAll(perms []Permission) []Permission {
	concrete := make(map[Permission]struct{}, len(perms))
	for _, p := range perms {
		for _, member := range Expand(p) {
			concrete[member] = struct{}{}
		}
	}
	out := make([]Permission, 0, len(concrete))
	for p := range concrete {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
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
