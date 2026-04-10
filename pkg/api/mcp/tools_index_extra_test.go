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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// handleIndexList
// ---------------------------------------------------------------------------

func TestHandleIndexList_Success(t *testing.T) {
	indexSvc := &mockIndexService{
		listIndexesFn: func(_ context.Context) ([]string, error) {
			return []string{"idx-name", "idx-age", "idx-email"}, nil
		},
	}
	handler := handleIndexList(indexSvc)

	req := buildCallToolRequest(t, "qrdb_index_list", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleIndexList: %v", err)
	}

	text := resultText(t, result)
	var names []string
	if jsonErr := json.Unmarshal([]byte(text), &names); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if len(names) != 3 {
		t.Errorf("expected 3 indexes, got %d: %v", len(names), names)
	}
}

func TestHandleIndexList_Empty(t *testing.T) {
	indexSvc := &mockIndexService{
		listIndexesFn: func(_ context.Context) ([]string, error) {
			return []string{}, nil
		},
	}
	handler := handleIndexList(indexSvc)

	req := buildCallToolRequest(t, "qrdb_index_list", map[string]any{})

	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("handleIndexList: %v", err)
	}

	text := resultText(t, result)
	var names []string
	if jsonErr := json.Unmarshal([]byte(text), &names); jsonErr != nil {
		t.Fatalf("unmarshal: %v", jsonErr)
	}
	if len(names) != 0 {
		t.Errorf("expected 0 indexes, got %d", len(names))
	}
}

func TestHandleIndexList_ServiceError(t *testing.T) {
	indexSvc := &mockIndexService{
		listIndexesFn: func(_ context.Context) ([]string, error) {
			return nil, errors.New("list indexes failed")
		},
	}
	handler := handleIndexList(indexSvc)

	req := buildCallToolRequest(t, "qrdb_index_list", map[string]any{})

	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error from service")
	}

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Errorf("expected *ToolError, got %T", err)
	}
	if toolErr.Tool != "qrdb_index_list" {
		t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_index_list")
	}
}
