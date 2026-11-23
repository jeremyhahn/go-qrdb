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

package main

import (
	"bytes"
	"testing"
)

// TestPrintYAML_OutputUnchanged pins the CLI YAML output byte for byte for a
// nested map (keys sorted, four-space indent) and a struct with yaml tags.
func TestPrintYAML_OutputUnchanged(t *testing.T) {
	type entry struct {
		Name    string            `yaml:"name"`
		Count   int               `yaml:"count"`
		Enabled bool              `yaml:"enabled"`
		Tags    []string          `yaml:"tags,omitempty"`
		Labels  map[string]string `yaml:"labels,omitempty"`
		Skipped string            `yaml:"-"`
	}
	cases := []struct {
		name string
		in   any
		want string
	}{
		{
			name: "nested map",
			in: map[string]any{
				"zeta":  1,
				"alpha": map[string]any{"b": "two", "a": []any{"x", 2, true}},
				"empty": nil,
			},
			want: "alpha:\n    a:\n        - x\n        - 2\n        - true\n    b: two\nempty: null\nzeta: 1\n",
		},
		{
			name: "struct with yaml tags",
			in: entry{
				Name:    "docs",
				Count:   3,
				Enabled: true,
				Tags:    []string{"a", "b"},
				Labels:  map[string]string{"env": "test", "app": "qrdb"},
				Skipped: "hidden",
			},
			want: "name: docs\ncount: 3\nenabled: true\ntags:\n    - a\n    - b\nlabels:\n    app: qrdb\n    env: test\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			prev := outputWriter
			outputWriter = &buf
			t.Cleanup(func() { outputWriter = prev })
			if err := printYAML(tc.in); err != nil {
				t.Fatalf("printYAML error = %v", err)
			}
			if buf.String() != tc.want {
				t.Fatalf("printYAML output changed:\n got %q\nwant %q", buf.String(), tc.want)
			}
		})
	}
}
