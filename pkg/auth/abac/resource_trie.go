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

package abac

import "strings"

// ResourceTrie is a prefix tree for efficient resource pattern matching.
type ResourceTrie struct {
	root *trieNode
}

type trieNode struct {
	children     map[string]*trieNode
	isWildcard   bool
	isMultiLevel bool
	isVariable   bool
	variableName string
	value        interface{}
	isTerminal   bool
}

// NewResourceTrie creates a new empty ResourceTrie.
func NewResourceTrie() *ResourceTrie {
	return &ResourceTrie{
		root: &trieNode{
			children: make(map[string]*trieNode),
		},
	}
}

// Insert adds a resource pattern to the trie with an associated value.
// Patterns support:
//   - Exact segments: "keys/config/database"
//   - Single-level wildcard: "keys/users/*/profile"
//   - Multi-level wildcard: "keys/tenant/**"
//   - Variable capture: "keys/tenant/${varName}/data" or "keys/tenant/:varName/data"
//   - Glob patterns: "keys/reports/2024-*-final"
func (rt *ResourceTrie) Insert(pattern string, value interface{}) {
	segments := splitPath(pattern)
	node := rt.root

	for _, segment := range segments {
		node = rt.insertSegment(node, segment)
	}

	node.isTerminal = true
	node.value = value
}

// insertSegment inserts a single path segment into the trie.
func (rt *ResourceTrie) insertSegment(node *trieNode, segment string) *trieNode {
	// Handle multi-level wildcard
	if segment == "**" {
		if child, exists := node.children["**"]; exists {
			return child
		}
		child := &trieNode{
			children:     make(map[string]*trieNode),
			isMultiLevel: true,
		}
		node.children["**"] = child
		return child
	}

	// Handle single-level wildcard
	if segment == "*" {
		if child, exists := node.children["*"]; exists {
			return child
		}
		child := &trieNode{
			children:   make(map[string]*trieNode),
			isWildcard: true,
		}
		node.children["*"] = child
		return child
	}

	// Handle variable capture: ${varName} or :varName
	if isVariable(segment) {
		varName := extractVariableName(segment)
		key := "${" + varName + "}"

		if child, exists := node.children[key]; exists {
			return child
		}
		child := &trieNode{
			children:     make(map[string]*trieNode),
			isVariable:   true,
			variableName: varName,
		}
		node.children[key] = child
		return child
	}

	// Handle exact or glob pattern segments
	if child, exists := node.children[segment]; exists {
		return child
	}
	child := &trieNode{
		children: make(map[string]*trieNode),
	}
	node.children[segment] = child
	return child
}

// Match attempts to match a resource path against stored patterns.
// Returns the associated value, captured variables, and whether a match was found.
// Matching priority: exact > variable > single wildcard > multi-level wildcard.
func (rt *ResourceTrie) Match(resource string) (value interface{}, variables map[string]string, found bool) {
	segments := splitPath(resource)
	variables = make(map[string]string)

	// Try to find the best match (highest priority)
	return rt.matchSegments(rt.root, segments, variables, 0)
}

// matchSegments recursively matches resource segments against the trie.
func (rt *ResourceTrie) matchSegments(node *trieNode, segments []string, variables map[string]string, depth int) (interface{}, map[string]string, bool) {
	// Base case: consumed all segments
	if depth == len(segments) {
		if node.isTerminal {
			return node.value, variables, true
		}
		return nil, nil, false
	}

	currentSegment := segments[depth]
	var bestMatch interface{}
	var bestVars map[string]string
	bestFound := false
	bestPriority := 0

	// Priority 1: Exact match (including glob patterns)
	for key, child := range node.children {
		if key == currentSegment || (key != "*" && key != "**" && !strings.HasPrefix(key, "${") && matchGlob(key, currentSegment)) {
			value, vars, found := rt.matchSegments(child, segments, copyVars(variables), depth+1)
			if found && (!bestFound || 4 > bestPriority) {
				bestMatch = value
				bestVars = vars
				bestFound = true
				bestPriority = 4
			}
		}
	}

	// Priority 2: Variable capture
	for _, child := range node.children {
		if child.isVariable {
			newVars := copyVars(variables)
			newVars[child.variableName] = currentSegment
			value, vars, found := rt.matchSegments(child, segments, newVars, depth+1)
			if found && (!bestFound || 3 > bestPriority) {
				bestMatch = value
				bestVars = vars
				bestFound = true
				bestPriority = 3
			}
		}
	}

	// Priority 3: Single-level wildcard
	if child, exists := node.children["*"]; exists {
		value, vars, found := rt.matchSegments(child, segments, copyVars(variables), depth+1)
		if found && (!bestFound || 2 > bestPriority) {
			bestMatch = value
			bestVars = vars
			bestFound = true
			bestPriority = 2
		}
	}

	// Priority 4: Multi-level wildcard
	if child, exists := node.children["**"]; exists {
		// Multi-level wildcard can match one or more segments
		// Try matching remaining segments
		for i := depth; i < len(segments); i++ {
			// Try to match from position i+1
			value, vars, found := rt.matchSegments(child, segments, copyVars(variables), i+1)
			if found && !bestFound {
				bestMatch = value
				bestVars = vars
				bestFound = true
				// Note: bestPriority stays at lowest since multi-level is lowest priority
			}
		}

		// Also try matching at the end (multi-level as terminal)
		if child.isTerminal && !bestFound {
			bestMatch = child.value
			bestVars = copyVars(variables)
			bestFound = true
		}
	}

	return bestMatch, bestVars, bestFound
}

// Delete removes a pattern from the trie.
// Returns true if the pattern was found and deleted, false otherwise.
func (rt *ResourceTrie) Delete(pattern string) bool {
	segments := splitPath(pattern)
	return rt.deleteSegments(rt.root, segments, 0)
}

// deleteSegments recursively deletes a pattern from the trie.
func (rt *ResourceTrie) deleteSegments(node *trieNode, segments []string, depth int) bool {
	if depth == len(segments) {
		if node.isTerminal {
			node.isTerminal = false
			node.value = nil
			return true
		}
		return false
	}

	segment := segments[depth]
	key := segment

	// Normalize variable segment to stored key format
	if isVariable(segment) {
		varName := extractVariableName(segment)
		key = "${" + varName + "}"
	}

	child, exists := node.children[key]
	if !exists {
		return false
	}

	deleted := rt.deleteSegments(child, segments, depth+1)

	// Clean up empty nodes
	if deleted && !child.isTerminal && len(child.children) == 0 {
		delete(node.children, key)
	}

	return deleted
}

// Clear removes all patterns from the trie.
func (rt *ResourceTrie) Clear() {
	rt.root = &trieNode{
		children: make(map[string]*trieNode),
	}
}

// Size returns the number of patterns stored in the trie.
func (rt *ResourceTrie) Size() int {
	return rt.countTerminals(rt.root)
}

// countTerminals counts terminal nodes in the trie.
func (rt *ResourceTrie) countTerminals(node *trieNode) int {
	count := 0
	if node.isTerminal {
		count = 1
	}
	for _, child := range node.children {
		count += rt.countTerminals(child)
	}
	return count
}

// splitPath splits a resource path into segments.
func splitPath(path string) []string {
	if path == "" {
		return []string{}
	}
	return strings.Split(strings.Trim(path, "/"), "/")
}

// isVariable checks if a segment is a variable placeholder.
func isVariable(segment string) bool {
	return (strings.HasPrefix(segment, "${") && strings.HasSuffix(segment, "}")) ||
		(strings.HasPrefix(segment, ":") && len(segment) > 1) ||
		(strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}"))
}

// extractVariableName extracts the variable name from a placeholder.
func extractVariableName(segment string) string {
	if strings.HasPrefix(segment, "${") && strings.HasSuffix(segment, "}") {
		return segment[2 : len(segment)-1]
	}
	if strings.HasPrefix(segment, ":") {
		return segment[1:]
	}
	if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
		return segment[1 : len(segment)-1]
	}
	return segment
}

// matchGlob performs simple glob pattern matching with * wildcard.
// This handles glob patterns within a single segment (e.g., "2024-*-final").
func matchGlob(pattern, str string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == str
	}

	parts := strings.Split(pattern, "*")
	pos := 0

	for i, part := range parts {
		if part == "" {
			continue
		}

		idx := strings.Index(str[pos:], part)
		if idx == -1 {
			return false
		}

		// First part must match at the beginning
		if i == 0 && idx != 0 {
			return false
		}

		pos += idx + len(part)
	}

	// Last part must match at the end
	if len(parts) > 0 && parts[len(parts)-1] != "" {
		return strings.HasSuffix(str, parts[len(parts)-1])
	}

	return true
}

// copyVars creates a copy of the variables map.
func copyVars(vars map[string]string) map[string]string {
	if vars == nil {
		return make(map[string]string)
	}
	newMap := make(map[string]string, len(vars))
	for k, v := range vars {
		newMap[k] = v
	}
	return newMap
}
