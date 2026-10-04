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

//go:build integration

package integration_test

// The replica-forwarding suite's MCP client. MCP has no stream: qrdb_watch
// collects events for a bounded time and answers once, so it is forwarded
// like any other tool call.

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net/http"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpgo "github.com/mark3labs/mcp-go/mcp"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
)

type mcpFwdTransport struct{}

func (mcpFwdTransport) name() string { return "mcp" }

func (mcpFwdTransport) reportsServedBy() bool { return true }

func (mcpFwdTransport) hasStream() bool { return false }

// call invokes tool on node i as caller and returns the answer.
func (mcpFwdTransport) call(t *testing.T, c *fwdCluster, i int, caller fwdCaller, tool string, args map[string]any) fwdAnswer {
	t.Helper()
	certPath, keyPath := c.certFor(caller)
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: c.ca.Pool, Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13,
	}}}
	client, err := mcpclient.NewStreamableHttpClient(fmt.Sprintf("https://127.0.0.1:%d/mcp", c.nodes[i].mcp), transport.WithHTTPBasicClient(hc))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		return fwdAnswer{detail: err.Error()}
	}
	init := mcpgo.InitializeRequest{}
	init.Params.ProtocolVersion = mcpgo.LATEST_PROTOCOL_VERSION
	init.Params.ClientInfo = mcpgo.Implementation{Name: "replica-forwarding-suite", Version: "1"}
	if _, err := client.Initialize(ctx, init); err != nil {
		// A caller the node cannot authenticate is refused before any tool.
		return fwdAnswer{detail: err.Error()}
	}
	req := mcpgo.CallToolRequest{}
	req.Params.Name = tool
	req.Params.Arguments = args
	res, err := client.CallTool(ctx, req)
	if err != nil {
		return fwdAnswer{detail: err.Error()}
	}
	a := fwdAnswer{ok: !res.IsError, detail: "ok"}
	if res.Meta != nil {
		a.servedBy, _ = res.Meta.AdditionalFields["x-qrdb-forwarded-to"].(string)
	}
	if text, isText := firstText(res); isText {
		a.value = text
		if res.IsError {
			a.detail = text
		}
	}
	if p, coded := wire.DecodeErrorPayload(res.StructuredContent); coded {
		a.denied = p.Code == wire.CodePermissionDenied
		a.notFound = p.Code == wire.CodeNotFound
		if p.Reason == "NOT_A_REPLICA" && p.Code == wire.CodeUnavailable {
			a.refusal = p.Metadata
		}
	}
	return a
}

// firstText is a tool result's first text content.
func firstText(res *mcpgo.CallToolResult) (string, bool) {
	for _, c := range res.Content {
		if tc, ok := mcpgo.AsTextContent(c); ok {
			return tc.Text, true
		}
	}
	return "", false
}

func (m mcpFwdTransport) put(t *testing.T, c *fwdCluster, node int, caller fwdCaller, db, key, value string) fwdAnswer {
	t.Helper()
	return m.call(t, c, node, caller, "qrdb_put", map[string]any{
		"key": key, "value": base64.StdEncoding.EncodeToString([]byte(value)), "database": db,
	})
}

func (m mcpFwdTransport) get(t *testing.T, c *fwdCluster, node int, caller fwdCaller, db, key string) fwdAnswer {
	t.Helper()
	a := m.call(t, c, node, caller, "qrdb_get", map[string]any{"key": key, "database": db})
	if a.ok {
		raw, err := base64.StdEncoding.DecodeString(a.value)
		if err != nil {
			t.Fatalf("decode qrdb_get value %q: %v", a.value, err)
		}
		a.value = string(raw)
	}
	return a
}

func (m mcpFwdTransport) watch(t *testing.T, c *fwdCluster, node int, db string) fwdAnswer {
	t.Helper()
	return m.call(t, c, node, fwdAsWriter, "qrdb_watch", map[string]any{"key": "k", "database": db, "timeout_seconds": 0.2})
}
