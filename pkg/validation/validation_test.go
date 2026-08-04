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

package validation

import (
	"strings"
	"testing"
)

// TestSanitizeForLog verifies control-character stripping and truncation.
func TestSanitizeForLog(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"clean string", "hello world", "hello world"},
		{"with newline", "hello\nworld", "helloworld"},
		{"with tab", "hello\tworld", "helloworld"},
		{"with null byte", "hello\x00world", "helloworld"},
		{"with del character", "hello\x7fworld", "helloworld"},
		{"with multiple controls", "hello\n\r\t\x00world", "helloworld"},
		{"very long string", strings.Repeat("a", 1500), strings.Repeat("a", 1000) + "...[truncated]"},
		{"empty string", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if result := SanitizeForLog(tt.input); result != tt.expected {
				t.Errorf("SanitizeForLog(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestSanitizeForLogAttackVectors verifies log-injection payloads are
// neutralized before they can forge log entries.
func TestSanitizeForLogAttackVectors(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"log injection newline", "key\nINFO: fake log"},
		{"log injection carriage return", "key\rINFO: fake"},
		{"terminal escape sequence", "key\x1b[31mred"},
		{"null byte smuggling", "key\x00.txt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeForLog(tt.input)
			for _, r := range result {
				if r < 32 || r == 127 {
					t.Errorf("SanitizeForLog(%q) = %q still contains control character %q", tt.input, result, r)
				}
			}
		})
	}
}

// BenchmarkSanitizeForLog measures the sanitization hot path.
func BenchmarkSanitizeForLog(b *testing.B) {
	input := "hello world with some text"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = SanitizeForLog(input)
	}
}
