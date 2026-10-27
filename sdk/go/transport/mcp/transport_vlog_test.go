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

//go:build qrdb_vlog

package mcp

import (
	"context"
	"testing"

	mcpapi "github.com/jeremyhahn/go-qrdb/pkg/api/mcp"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/vlogtest"
)

// connectVlogInProcess is an SDK MCP client of the REAL pkg/api/mcp server
// over an in-memory log service.
func connectVlogInProcess(t *testing.T) *Transport {
	t.Helper()
	srv := mcpapi.NewServer(newMockKVService(), newMockAdminService(), ":0", mcpapi.WithLogService(vlogtest.NewService(t)))
	tr, err := NewInProcess(srv.MCPServer())
	if err != nil {
		t.Fatalf("NewInProcess: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func TestVlog_RoundTripThroughTheServerTools(t *testing.T) {
	vlogtest.Run(t, connectVlogInProcess(t))
}

func TestVlog_Guards(t *testing.T) {
	closed := connectVlogInProcess(t)
	_ = closed.Close()
	vlogtest.RunGuards(t, connectVlogInProcess(t), closed)
}
