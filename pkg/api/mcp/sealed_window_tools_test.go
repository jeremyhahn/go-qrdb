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
	"testing"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

type fixedListenerProbe struct{ sealed bool }

func (p fixedListenerProbe) IsSealed() bool                    { return p.sealed }
func (fixedListenerProbe) EnrollmentOpen(context.Context) bool { return false }
func (fixedListenerProbe) Admit(bool) func()                   { return func() {} }

// A sealed window exports the unseal surface and nothing else: neither health
// nor status, whether or not a barrier service is wired.
func TestSealedWindowTools_ExportNoHealthAndNoNodeStatus(t *testing.T) {
	svc := &capturingMCPBarrierSvc{}
	for _, barrier := range []service.BarrierServicer{nil, svc} {
		exportsBarrierStatus := false
		for _, tl := range SealedWindowTools(barrier, nil, nil, fixedListenerProbe{sealed: true}) {
			switch tl.Tool.Name {
			case "qrdb_health", "qrdb_status", "qrdb_ceremony_status":
				t.Errorf("a sealed window exports %s (barrier=%v)", tl.Tool.Name, barrier != nil)
			case "qrdb_barrier_status":
				exportsBarrierStatus = true
			}
		}
		// Barrier status is the pre-init answer; the probe decides per call
		// whether it is served.
		if !exportsBarrierStatus {
			t.Errorf("a sealed window does not export qrdb_barrier_status (barrier=%v)", barrier != nil)
		}
	}
	var unseal *mcpserver.ServerTool
	for _, tl := range SealedWindowTools(svc, nil, nil, fixedListenerProbe{sealed: true}) {
		if tl.Tool.Name == "qrdb_barrier_unseal" {
			unseal = &tl
		}
	}
	if unseal == nil {
		t.Fatal("a sealed window does not export qrdb_barrier_unseal")
	}
	res, err := unseal.Handler(context.Background(), buildCallToolRequest(t, "qrdb_barrier_unseal", map[string]any{"secret": "s3cret"}))
	if err != nil || res.IsError {
		t.Fatalf("unseal: res=%+v err=%v", res, err)
	}
	if len(svc.seen) == 0 {
		t.Fatal("the unseal tool never reached the barrier service")
	}
}

// A sealed listener registers every built-in tool it does not serve as the
// typed sealed refusal, so a data, health or status call reaches its gate and
// answers UNAVAILABLE/SEALED rather than "tool not found".
func TestSealedWindowServer_RefusesUnservedToolsAsSealed(t *testing.T) {
	srv := NewSealedWindowServer("127.0.0.1:0", &capturingMCPBarrierSvc{}, nil, nil, fixedListenerProbe{sealed: true})
	for _, name := range []string{"qrdb_get", "qrdb_put", "qrdb_health", "qrdb_status"} {
		tl := srv.mcpSrv.GetTool(name)
		if tl == nil {
			t.Errorf("%s: not registered on the sealed listener", name)
			continue
		}
		res, err := srv.sealedWindowMiddleware()(tl.Handler)(context.Background(), buildCallToolRequest(t, name, map[string]any{}))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		payload, ok := wire.DecodeErrorPayload(res.StructuredContent)
		if !res.IsError || !ok || payload.Code != wire.CodeUnavailable || payload.Reason != sealedwindow.ReasonSealed {
			t.Errorf("%s: res=%+v payload=%+v, want UNAVAILABLE/SEALED", name, res, payload)
		}
	}
}

// The gate travels with each exported handler: once the barrier is unsealed
// the tool refuses with CodeUnavailable instead of answering.
func TestSealedWindowTools_RefuseOnceUnsealed(t *testing.T) {
	svc := &capturingMCPBarrierSvc{}
	var unseal *mcpserver.ServerTool
	for _, tl := range SealedWindowTools(svc, nil, nil, fixedListenerProbe{sealed: false}) {
		if tl.Tool.Name == "qrdb_barrier_unseal" {
			unseal = &tl
		}
	}
	if unseal == nil {
		t.Fatal("qrdb_barrier_unseal not exported")
	}
	res, err := unseal.Handler(context.Background(), buildCallToolRequest(t, "qrdb_barrier_unseal", map[string]any{"secret": "s3cret"}))
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("an unsealed window answered %q", res.Content)
	}
	if svc.seen != nil {
		t.Fatal("an unsealed window reached the barrier service")
	}
}
