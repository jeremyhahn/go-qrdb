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
	"testing"
	"time"

	mcpserver "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sotest"
)

// TestSOPINConformance: an SO rekeys and deletes recovery keys through the
// SDK MCP transport against the real MCP server with the SO gate on. The PIN
// travels in the X-QRDB-SO-PIN header of the /mcp request.
func TestSOPINConformance(t *testing.T) {
	sotest.Run(t, func(t *testing.T, f *sotest.Fixture) transport.BarrierClient {
		t.Helper()
		srv := mcpserver.NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
			mcpserver.WithTLS(f.ServerTLS), mcpserver.WithAuthMiddleware(f.MW),
			mcpserver.WithBarrierService(f.Barrier), mcpserver.WithPINBackend(f.PINs), mcpserver.WithSOGate(f.Gate))
		if err := srv.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
		t.Cleanup(srv.Stop)
		tr, err := New(transport.WithAddress("https://"+srv.Addr()+"/mcp"), transport.WithTLSConfig(f.ClientTLS), transport.WithTimeout(5*time.Second))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := tr.Connect(context.Background()); err != nil {
			t.Fatalf("Connect: %v", err)
		}
		t.Cleanup(func() { _ = tr.Close() })
		return tr
	})
}
