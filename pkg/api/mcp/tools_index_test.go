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
	"reflect"
	"testing"
)

// ---------------------------------------------------------------------------
// handleIndexList
// ---------------------------------------------------------------------------

func TestHandleIndexList(t *testing.T) {
	tests := []struct {
		name    string
		names   []string
		listErr error
		wantRaw string
	}{
		{name: "populated", names: []string{"idx_a", "idx_b"}, wantRaw: `["idx_a","idx_b"]`},
		{name: "empty", names: []string{}, wantRaw: `[]`},
		{name: "service error", listErr: errors.New("list indexes failed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			indexSvc := &mockIndexService{
				listIndexesFn: func(_ context.Context) ([]string, error) {
					return tt.names, tt.listErr
				},
			}
			handler := handleIndexList(indexSvc)

			req := buildCallToolRequest(t, "qrdb_index_list", map[string]any{})

			result, err := handler(context.Background(), req)
			if tt.listErr != nil {
				if err == nil {
					t.Fatal("expected error from service")
				}
				var toolErr *ToolError
				if !errors.As(err, &toolErr) {
					t.Fatalf("expected *ToolError, got %T", err)
				}
				if toolErr.Tool != "qrdb_index_list" {
					t.Errorf("tool: got %q, want %q", toolErr.Tool, "qrdb_index_list")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			text := resultText(t, result)
			if text != tt.wantRaw {
				t.Errorf("raw response: got %s, want %s", text, tt.wantRaw)
			}
			var got []string
			if err := json.Unmarshal([]byte(text), &got); err != nil {
				t.Fatalf("decode response %q: %v", text, err)
			}
			if !reflect.DeepEqual(got, tt.names) {
				t.Errorf("index names: got %v, want %v", got, tt.names)
			}
		})
	}
}
