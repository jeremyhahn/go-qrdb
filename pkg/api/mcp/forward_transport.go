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
	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// The streamable HTTP headers (MCP 2025-06-18, Transports).
const (
	headerMCPSessionID       = "Mcp-Session-Id"
	headerMCPProtocolVersion = "Mcp-Protocol-Version"
)

// forwardCloseTimeout bounds the DELETE that ends the session on Close.
const forwardCloseTimeout = 2 * time.Second

// forwardErrorBodyLimit bounds how much of a failed reply is quoted.
const forwardErrorBodyLimit = 512

// errForwardRedirect is the forward's answer to any 3xx: every request
// carries a freshly signed on-behalf assertion, so following a peer's
// Location would hand that assertion to whichever host it names.
var errForwardRedirect = errors.New("mcp forward: the peer answered with a redirect")

// forwardTransport is the client side of the MCP streamable HTTP transport
// for replica forwarding. Unlike mcp-go's transport.NewStreamableHTTP it
// never follows a redirect (GO-2026-6911): its http.Client refuses every
// one, and any 3xx reply is a forward failure. It POSTs each JSON-RPC
// message, keeps the Mcp-Session-Id the server assigns, and reads replies
// sent as application/json or as a text/event-stream.
type forwardTransport struct {
	url    string
	client *http.Client

	mu              sync.RWMutex
	sessionID       string
	protocolVersion string
	onNotification  func(mcpgo.JSONRPCNotification)
}

var _ mcptransport.HTTPConnection = (*forwardTransport)(nil)

// newForwardTransport returns a transport that sends to url through rt.
func newForwardTransport(url string, rt http.RoundTripper) *forwardTransport {
	return &forwardTransport{
		url: url,
		client: &http.Client{
			Transport:     rt,
			CheckRedirect: func(*http.Request, []*http.Request) error { return errForwardRedirect },
		},
	}
}

// Start implements transport.Interface; the transport has no standing
// connection to open.
func (t *forwardTransport) Start(context.Context) error { return nil }

// GetSessionId implements transport.Interface.
func (t *forwardTransport) GetSessionId() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.sessionID
}

// SetProtocolVersion implements transport.HTTPConnection.
func (t *forwardTransport) SetProtocolVersion(version string) {
	t.mu.Lock()
	t.protocolVersion = version
	t.mu.Unlock()
}

// SetNotificationHandler implements transport.Interface.
func (t *forwardTransport) SetNotificationHandler(handler func(mcpgo.JSONRPCNotification)) {
	t.mu.Lock()
	t.onNotification = handler
	t.mu.Unlock()
}

// SendRequest implements transport.Interface.
func (t *forwardTransport) SendRequest(ctx context.Context, request mcptransport.JSONRPCRequest) (*mcptransport.JSONRPCResponse, error) {
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
		return nil, forwardStatusError(resp)
	}
	if request.Method == string(mcpgo.MethodInitialize) {
		if id := resp.Header.Get(headerMCPSessionID); id != "" {
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
			return nil, fmt.Errorf("mcp forward: decode the reply: %w", err)
		}
		return &out, nil
	case "text/event-stream":
		return t.readEventStream(resp.Body, request.ID)
	default:
		return nil, fmt.Errorf("mcp forward: unexpected reply content type %q", mediaType)
	}
}

// SendNotification implements transport.Interface.
func (t *forwardTransport) SendNotification(ctx context.Context, notification mcpgo.JSONRPCNotification) error {
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
		return forwardStatusError(resp)
	}
	return nil
}

// Close implements transport.Interface: it ends the server's session, if
// one was opened.
func (t *forwardTransport) Close() error {
	sessionID := t.GetSessionId()
	if sessionID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), forwardCloseTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, t.url, nil)
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

func (t *forwardTransport) post(ctx context.Context, body []byte, header http.Header) (*http.Response, error) {
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

func (t *forwardTransport) setSessionHeaders(req *http.Request) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.sessionID != "" {
		req.Header.Set(headerMCPSessionID, t.sessionID)
	}
	if t.protocolVersion != "" {
		req.Header.Set(headerMCPProtocolVersion, t.protocolVersion)
	}
}

// forwardMessage is any JSON-RPC message the server sends on a stream.
type forwardMessage struct {
	ID     *mcpgo.RequestId `json:"id,omitempty"`
	Method string           `json:"method,omitempty"`
}

// readEventStream reads server-sent events until the reply to id arrives,
// handing the notifications sent before it to the notification handler.
func (t *forwardTransport) readEventStream(body io.Reader, id mcpgo.RequestId) (*mcptransport.JSONRPCResponse, error) {
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
			return nil, errors.New("mcp forward: the event stream ended without a reply")
		}
	}
}

// dispatchEvent returns the event's message if it is the reply to id, and
// otherwise hands a notification to the handler and returns nil. Requests
// from the server are not answered: a forward is a single tool call.
func (t *forwardTransport) dispatchEvent(data []byte, id mcpgo.RequestId) *mcptransport.JSONRPCResponse {
	var msg forwardMessage
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
		var n mcpgo.JSONRPCNotification
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

func forwardStatusError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, forwardErrorBodyLimit))
	return fmt.Errorf("mcp forward: the peer answered %s: %s", resp.Status, strings.TrimSpace(string(b)))
}
