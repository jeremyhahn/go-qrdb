// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
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

	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

type fixedListenerProbe struct{ sealed bool }

func (p fixedListenerProbe) IsSealed() bool                    { return p.sealed }
func (fixedListenerProbe) EnrollmentOpen(context.Context) bool { return false }
func (fixedListenerProbe) Admit(bool) func()                   { return func() {} }

// With no services the only sealed tool a window can serve is health, and it
// answers the sealed verdict through the gate.
func TestSealedWindowTools_WithoutServicesExportsHealthOnly(t *testing.T) {
	tools := SealedWindowTools(nil, nil, nil, fixedListenerProbe{sealed: true})
	if len(tools) != 1 || tools[0].Tool.Name != "qrdb_health" {
		names := make([]string, 0, len(tools))
		for _, tl := range tools {
			names = append(names, tl.Tool.Name)
		}
		t.Fatalf("tools = %v, want only qrdb_health", names)
	}
	res, err := tools[0].Handler(context.Background(), mcpgo.CallToolRequest{Params: mcpgo.CallToolParams{Name: "qrdb_health"}})
	if err != nil || res.IsError {
		t.Fatalf("health: res=%+v err=%v", res, err)
	}
	tc, ok := res.Content[0].(mcpgo.TextContent)
	if !ok {
		t.Fatalf("health content = %+v, want text", res.Content)
	}
	var hs service.HealthStatus
	if err := json.Unmarshal([]byte(tc.Text), &hs); err != nil || hs.Status != service.HealthStatusSealed {
		t.Fatalf("health = %s, want status %q", tc.Text, service.HealthStatusSealed)
	}
}

// The gate travels with each exported handler: once the barrier is unsealed
// the tool refuses with CodeUnavailable instead of answering.
func TestSealedWindowTools_RefuseOnceUnsealed(t *testing.T) {
	tools := SealedWindowTools(nil, nil, nil, fixedListenerProbe{sealed: false})
	res, err := tools[0].Handler(context.Background(), mcpgo.CallToolRequest{Params: mcpgo.CallToolParams{Name: "qrdb_health"}})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("an unsealed window answered %q", res.Content)
	}
}
