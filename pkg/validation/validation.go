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

// Package validation provides the shared, domain-neutral input validation
// framework for the stack: the Constraint vocabulary, the ErrValidation
// typed error that domain packages embed in their own error types, and
// generic sanitization helpers. Domain-specific validators (key IDs,
// backend names, and similar) live in the packages that own those domains
// and build on the primitives defined here.
package validation

import "strings"

// SanitizeForLog sanitizes a string for safe logging (prevents log injection).
// Control characters and null bytes are removed, and the result is truncated
// to 1000 bytes to prevent log flooding.
func SanitizeForLog(s string) string {
	// Remove control characters and null bytes
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, s)

	// Limit length to prevent log flooding
	if len(s) > 1000 {
		s = s[:1000] + "...[truncated]"
	}

	return s
}
