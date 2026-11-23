//go:build !integration

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

package mcp

import (
	"slices"
	"testing"
)

// TestVectorTools_SchemaHasNoShardID asserts that no vector tool's input
// schema names shard_id: the database's binding decides the vector shard
// (OD-14 A).
func TestVectorTools_SchemaHasNoShardID(t *testing.T) {
	tools := vectorTools(&mockVectorService{})
	if len(tools) == 0 {
		t.Fatal("no vector tools registered")
	}
	for _, tool := range tools {
		schema := tool.Tool.InputSchema
		if _, ok := schema.Properties["shard_id"]; ok {
			t.Errorf("tool %q input schema has property shard_id", tool.Tool.Name)
		}
		if slices.Contains(schema.Required, "shard_id") {
			t.Errorf("tool %q input schema requires shard_id", tool.Tool.Name)
		}
	}
}
