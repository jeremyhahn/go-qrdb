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
	"context"
	"encoding/json"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/compaction"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

func TestDatabaseCompactionTool_UpdateAndClear(t *testing.T) {
	svc := newRealDatabaseService(t)
	ctx := context.Background()

	if _, err := handleDatabaseCreate(svc)(ctx, buildCallToolRequest(t, "qrdb_database_create", map[string]any{
		"name": "events", "engine": "kv", "backend": "memory",
	})); err != nil {
		t.Fatalf("create: %v", err)
	}

	handler := handleDatabaseUpdateCompaction(svc)
	result, err := handler(ctx, buildCallToolRequest(t, "qrdb_database_update_compaction", map[string]any{
		"name": "events", "mode": "period", "retainPeriod": "2h",
	}))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	var info database.DatabaseInfo
	if err := json.Unmarshal([]byte(resultText(t, result)), &info); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if info.Compaction == nil || info.Compaction.Mode != compaction.Period || info.Compaction.RetainPeriod != "2h" {
		t.Fatalf("updated compaction = %+v", info.Compaction)
	}

	result, err = handler(ctx, buildCallToolRequest(t, "qrdb_database_update_compaction", map[string]any{
		"name": "events", "mode": "revision", "retainRevisions": 3,
	}))
	if err != nil {
		t.Fatalf("update(revision): %v", err)
	}
	info = database.DatabaseInfo{}
	if err := json.Unmarshal([]byte(resultText(t, result)), &info); err != nil || info.Compaction.RetainRevisions != 3 {
		t.Fatalf("revision compaction = %+v, %v", info.Compaction, err)
	}

	result, err = handler(ctx, buildCallToolRequest(t, "qrdb_database_update_compaction", map[string]any{"name": "events"}))
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	info = database.DatabaseInfo{}
	if err := json.Unmarshal([]byte(resultText(t, result)), &info); err != nil || info.Compaction != nil {
		t.Fatalf("cleared compaction = %+v, %v", info.Compaction, err)
	}
}

func TestDatabaseCompactionTool_Refusals(t *testing.T) {
	svc := newRealDatabaseService(t)
	ctx := context.Background()
	handler := handleDatabaseUpdateCompaction(svc)

	result, err := handler(ctx, buildCallToolRequest(t, "qrdb_database_update_compaction", map[string]any{"mode": "off"}))
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("missing name = %+v, %v; want a tool error result", result, err)
	}
	if _, err := handler(ctx, buildCallToolRequest(t, "qrdb_database_update_compaction", map[string]any{
		"name": "nope", "mode": "revision", "retainRevisions": 1,
	})); err == nil {
		t.Fatal("a missing database took a policy")
	}
	if tool := toolDatabaseUpdateCompaction(); tool.Name != "qrdb_database_update_compaction" {
		t.Fatalf("tool name = %q", tool.Name)
	}
}
