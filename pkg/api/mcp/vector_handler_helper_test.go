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
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Helper: find tool handler by name from tool list
// ---------------------------------------------------------------------------

func findVectorHandler(t *testing.T, svc service.VectorServicer, toolName string) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	t.Helper()
	tools := vectorTools(svc)
	for _, tool := range tools {
		if tool.Tool.Name == toolName {
			return tool.Handler
		}
	}
	t.Fatalf("tool %q not found in vectorTools", toolName)
	return nil
}
