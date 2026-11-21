// Copyright 2026 Jeremy Hahn
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

package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestHandlePermissionsCatalog_MatchesRegistry(t *testing.T) {
	want, err := transport.PermissionCatalog()
	if err != nil {
		t.Fatalf("PermissionCatalog: %v", err)
	}
	res, err := handlePermissionsCatalog()(context.Background(), mcp.CallToolRequest{})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("content %T, want TextContent", res.Content[0])
	}
	var got struct {
		Permissions []transport.PermissionInfo `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(text.Text), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Permissions) != len(want) {
		t.Fatalf("got %d, want %d", len(got.Permissions), len(want))
	}
	for i := range want {
		if got.Permissions[i] != want[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, got.Permissions[i], want[i])
		}
	}
}
