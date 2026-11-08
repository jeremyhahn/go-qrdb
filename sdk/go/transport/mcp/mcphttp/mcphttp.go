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

// Package mcphttp is the client side of the MCP streamable HTTP transport
// (MCP 2025-06-18, Transports) that qrdb's MCP clients run on.
//
// It replaces mcp-go's transport.NewStreamableHTTP, which carries
// GO-2026-6911 (an unvalidated Location header) with no fixed release.
// Here the caller's http.Client alone decides whether a redirect is
// followed, and a 3xx it does not follow is a failure.
package mcphttp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// The streamable HTTP headers.
const (
	HeaderSessionID       = "Mcp-Session-Id"
	HeaderProtocolVersion = "Mcp-Protocol-Version"
)

// closeTimeout bounds the DELETE that ends the session on Close.
const closeTimeout = 2 * time.Second

// errorBodyLimit bounds how much of a failed reply is quoted.
const errorBodyLimit = 512

// Transport implements mcp-go's transport.Interface over streamable HTTP.
// It POSTs each JSON-RPC message, keeps the Mcp-Session-Id the server
// assigns, and reads replies sent as application/json or as a
// text/event-stream. Requests the server sends on a stream are not
// answered: the transport offers the server no client capabilities.
type Transport struct {
	url    string
	client *http.Client

	mu              sync.RWMutex
	sessionID       string
	protocolVersion string
	onNotification  func(mcp.JSONRPCNotification)
}

var _ mcptransport.HTTPConnection = (*Transport)(nil)

// New returns a transport that sends to url through client. The client's
// CheckRedirect is the redirect policy.
func New(url string, client *http.Client) *Transport {
	return &Transport{url: url, client: client}
}

// Start implements transport.Interface; there is no standing connection to
// open.
func (t *Transport) Start(context.Context) error { return nil }

// GetSessionId implements transport.Interface.
func (t *Transport) GetSessionId() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.sessionID
}

// SetProtocolVersion implements transport.HTTPConnection.
func (t *Transport) SetProtocolVersion(version string) {
	t.mu.Lock()
	t.protocolVersion = version
	t.mu.Unlock()
}

// SetNotificationHandler implements transport.Interface.
func (t *Transport) SetNotificationHandler(handler func(mcp.JSONRPCNotification)) {
	t.mu.Lock()
	t.onNotification = handler
	t.mu.Unlock()
}

// SendRequest implements transport.Interface.
func (t *Transport) SendRequest(ctx context.Context, request mcptransport.JSONRPCRequest) (*mcptransport.JSONRPCResponse, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	resp, err := t.post(ctx, body, request.Header)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp)
	}
	if request.Method == string(mcp.MethodInitialize) {
		if id := resp.Header.Get(HeaderSessionID); id != "" {
			t.mu.Lock()
			t.sessionID = id
			t.mu.Unlock()
		}
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch mediaType {
	case "application/json":
		var out mcptransport.JSONRPCResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil, fmt.Errorf("mcphttp: decode the reply: %w", err)
		}
		return &out, nil
	case "text/event-stream":
		return t.readEventStream(resp.Body, request.ID)
	default:
		return nil, fmt.Errorf("mcphttp: unexpected reply content type %q", mediaType)
	}
}

// SendNotification implements transport.Interface.
func (t *Transport) SendNotification(ctx context.Context, notification mcp.JSONRPCNotification) error {
	body, err := json.Marshal(notification)
	if err != nil {
		return err
	}
	resp, err := t.post(ctx, body, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return statusError(resp)
	}
	return nil
}

// Close implements transport.Interface: it ends the server's session, if
// one was opened.
func (t *Transport) Close() error {
	sessionID := t.GetSessionId()
	if sessionID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.url, http.NoBody)
	if err != nil {
		return err
	}
	t.setSessionHeaders(req)
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	t.mu.Lock()
	t.sessionID = ""
	t.mu.Unlock()
	return nil
}

func (t *Transport) post(ctx context.Context, body []byte, header http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	t.setSessionHeaders(req)
	return t.client.Do(req)
}

func (t *Transport) setSessionHeaders(req *http.Request) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.sessionID != "" {
		req.Header.Set(HeaderSessionID, t.sessionID)
	}
	if t.protocolVersion != "" {
		req.Header.Set(HeaderProtocolVersion, t.protocolVersion)
	}
}

// message is any JSON-RPC message the server sends on a stream.
type message struct {
	ID     *mcp.RequestId `json:"id,omitempty"`
	Method string         `json:"method,omitempty"`
}

// readEventStream reads server-sent events until the reply to id arrives,
// handing the notifications sent before it to the notification handler.
func (t *Transport) readEventStream(body io.Reader, id mcp.RequestId) (*mcptransport.JSONRPCResponse, error) {
	r := bufio.NewReader(body)
	var data strings.Builder
	for {
		line, err := r.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "" && data.Len() > 0:
			if resp := t.dispatchEvent([]byte(data.String()), id); resp != nil {
				return resp, nil
			}
			data.Reset()
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
		if errors.Is(err, io.EOF) {
			if data.Len() > 0 {
				if resp := t.dispatchEvent([]byte(data.String()), id); resp != nil {
					return resp, nil
				}
			}
			return nil, errors.New("mcphttp: the event stream ended without a reply")
		}
	}
}

// dispatchEvent returns the event's message if it is the reply to id, and
// otherwise hands a notification to the handler and returns nil.
func (t *Transport) dispatchEvent(data []byte, id mcp.RequestId) *mcptransport.JSONRPCResponse {
	var msg message
	if json.Unmarshal(data, &msg) != nil {
		return nil
	}
	switch {
	case msg.Method == "" && msg.ID != nil && msg.ID.String() == id.String():
		var resp mcptransport.JSONRPCResponse
		if json.Unmarshal(data, &resp) != nil {
			return nil
		}
		return &resp
	case msg.Method != "" && msg.ID == nil:
		var n mcp.JSONRPCNotification
		if json.Unmarshal(data, &n) != nil {
			return nil
		}
		t.mu.RLock()
		handler := t.onNotification
		t.mu.RUnlock()
		if handler != nil {
			handler(n)
		}
	}
	return nil
}

func statusError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
	return fmt.Errorf("mcphttp: the server answered %s: %s", resp.Status, strings.TrimSpace(string(b)))
}
