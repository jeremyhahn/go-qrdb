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

package mcphttp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync/atomic"

	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// Client is an MCP client session over a Transport: it opens the session
// with initialize and calls tools. It takes only mcp-go's JSON-RPC types,
// not its client or HTTP transports, so a caller that uses it reaches no
// mcp-go client code (GO-2026-6911).
type Client struct {
	transport *Transport
	nextID    atomic.Int64
}

// NewClient returns a Client that sends through t.
func NewClient(t *Transport) *Client {
	return &Client{transport: t}
}

// Start prepares the transport.
func (c *Client) Start(ctx context.Context) error {
	return c.transport.Start(ctx)
}

// Initialize opens the session: it sends initialize, checks the protocol
// version the server chose, and sends notifications/initialized.
func (c *Client) Initialize(ctx context.Context, request mcp.InitializeRequest) (*mcp.InitializeResult, error) {
	raw, err := c.call(ctx, mcp.MethodInitialize, request.Params)
	if err != nil {
		return nil, err
	}
	var result mcp.InitializeResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcphttp: decode the initialize result: %w", err)
	}
	if !slices.Contains(mcp.ValidProtocolVersions, result.ProtocolVersion) {
		return nil, fmt.Errorf("mcphttp: the server chose unsupported protocol version %q", result.ProtocolVersion)
	}
	c.transport.SetProtocolVersion(result.ProtocolVersion)
	if err := c.transport.SendNotification(ctx, mcp.JSONRPCNotification{
		JSONRPC:      mcp.JSONRPC_VERSION,
		Notification: mcp.Notification{Method: "notifications/initialized"},
	}); err != nil {
		return nil, fmt.Errorf("mcphttp: send notifications/initialized: %w", err)
	}
	return &result, nil
}

// CallTool calls a tool on the server. The session's protocol version and
// client identity are the ones initialize fixed, so the per-request
// protocol keys a forwarded request's _meta carries from its own
// connection are dropped.
func (c *Client) CallTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	params := request.Params
	params.Meta = sessionMeta(params.Meta)
	raw, err := c.call(ctx, mcp.MethodToolsCall, params)
	if err != nil {
		return nil, err
	}
	return mcp.ParseCallToolResult(&raw)
}

// Close ends the session.
func (c *Client) Close() error {
	return c.transport.Close()
}

// call sends one request and returns its result, or the server's JSON-RPC
// error as a Go error.
func (c *Client) call(ctx context.Context, method mcp.MCPMethod, params any) (json.RawMessage, error) {
	resp, err := c.transport.SendRequest(ctx, mcptransport.JSONRPCRequest{
		JSONRPC: mcp.JSONRPC_VERSION,
		ID:      mcp.NewRequestId(c.nextID.Add(1)),
		Method:  string(method),
		Params:  params,
	})
	if err != nil {
		return nil, fmt.Errorf("mcphttp: %s: %w", method, err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("mcphttp: %s: server error %d: %s", method, resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}

// requestProtocolKeys are the _meta keys protocol version 2026-07-28
// carries on every request in place of the initialize handshake.
var requestProtocolKeys = []string{
	mcp.MetaKeyProtocolVersion,
	mcp.MetaKeyClientInfo,
	mcp.MetaKeyClientCapabilities,
	mcp.MetaKeyLogLevel,
}

// sessionMeta returns a copy of meta without the per-request protocol keys,
// leaving the caller's meta untouched.
func sessionMeta(meta *mcp.Meta) *mcp.Meta {
	if meta == nil {
		return nil
	}
	out := &mcp.Meta{ProgressToken: meta.ProgressToken}
	for k, v := range meta.AdditionalFields {
		if slices.Contains(requestProtocolKeys, k) {
			continue
		}
		if out.AdditionalFields == nil {
			out.AdditionalFields = make(map[string]any, len(meta.AdditionalFields))
		}
		out.AdditionalFields[k] = v
	}
	if out.ProgressToken == nil && out.AdditionalFields == nil {
		return nil
	}
	return out
}
