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
	"bytes"
	"context"
	"crypto"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"sync/atomic"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	mcptransport "github.com/mark3labs/mcp-go/client/transport"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
)

// WithReplicaForwarding makes this listener forward a tool call it refuses
// because the node holds no replica of the database to a node that does, on
// the caller's behalf (docs/replica-forwarding): over MCP, to the replica's
// MCP endpoint. Without it the listener answers the refusal.
func WithReplicaForwarding(cfg replicaforward.Config) ServerOption {
	return func(s *Server) {
		c := cfg
		s.replicaForwarding = &c
	}
}

// mcpForwardClientName is the clientInfo name a forward initializes with.
const mcpForwardClientName = "qrdb-replica-forward"

// replicaForwarder forwards a tool call this node refused with NotAReplica
// to a replica's MCP endpoint as the caller, and relays the replica's
// result.
type replicaForwarder struct {
	cfg    replicaforward.Config
	key    crypto.Signer
	nodeID string
	base   *http.Transport
}

// newReplicaForwarder returns the forwarder for a listener serving nodeTLS:
// its certificate is the client certificate a forward presents and its key
// signs the caller assertion.
func newReplicaForwarder(cfg replicaforward.Config, nodeTLS *tls.Config) *replicaForwarder {
	key, nodeID := forward.NodeIdentity(nodeTLS)
	half := cfg.ForwardTimeout() / 2
	return &replicaForwarder{
		cfg:    cfg,
		key:    key,
		nodeID: nodeID,
		base: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:              cfg.RootCAs,
				Certificates:         nodeTLS.Certificates,
				GetClientCertificate: nodeTLS.GetClientCertificate,
				MinVersion:           tls.VersionTLS13,
			},
			ForceAttemptHTTP2:   true,
			DialContext:         (&net.Dialer{Timeout: half}).DialContext,
			TLSHandshakeTimeout: half,
		},
	}
}

// close releases the idle connections to targets.
func (f *replicaForwarder) close() { f.base.CloseIdleConnections() }

// middleware forwards a tool call next refused as a non-replica. It sits
// innermost, so the caller has been authenticated, authorized and scoped
// here before anything is forwarded; a call forwarded here on a caller's
// behalf is never forwarded again.
func (f *replicaForwarder) middleware() mcpserver.ToolHandlerMiddleware {
	return func(next mcpserver.ToolHandlerFunc) mcpserver.ToolHandlerFunc {
		return func(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			res, err := next(ctx, req)
			if err == nil || authmw.IsOnBehalf(auth.IdentityFromContext(ctx)) {
				return res, err
			}
			refusal, ok := errors.AsType[*placement.NotAReplicaError](err)
			if !ok {
				return res, err
			}
			if fwd, ferr, forwarded := f.forward(ctx, req, refusal); forwarded {
				return fwd, ferr
			}
			return res, err
		}
	}
}

// forward forwards req for the caller in ctx. It reports false when no
// replica served it -- the caller then gets the refusal, never a local
// answer. A failure after the call reached a replica is that failure.
func (f *replicaForwarder) forward(ctx context.Context, req mcpgo.CallToolRequest, refusal *placement.NotAReplicaError) (*mcpgo.CallToolResult, error, bool) {
	caller := auth.IdentityFromContext(ctx)
	if _, _, err := forward.SignCaller(caller, forward.HTTPBinding(http.MethodPost, "/mcp", nil), f.key, f.nodeID, time.Now()); err != nil {
		slog.Warn("replicaforward: not forwarded", "protocol", cluster.APIProtocolMCP, "tenant", refusal.TenantID, "database", refusal.Database, "error", err)
		return nil, nil, false
	}

	var result *mcpgo.CallToolResult
	send := func(ctx context.Context, t replicaforward.Target) (replicaforward.Result, error) {
		rt := &onBehalfTransport{base: f.base, caller: caller, key: f.key, nodeID: f.nodeID}
		cli, err := mcpclient.NewStreamableHttpClient("https://"+t.Endpoint+"/mcp", mcptransport.WithHTTPBasicClient(&http.Client{Transport: rt}))
		if err != nil {
			return replicaforward.Result{Outcome: replicaforward.Unreachable}, nil
		}
		defer func() { _ = cli.Close() }()
		if err := cli.Start(ctx); err != nil {
			return replicaforward.Result{Outcome: replicaforward.Unreachable}, nil
		}
		if _, err := cli.Initialize(ctx, mcpgo.InitializeRequest{Params: mcpgo.InitializeParams{
			ProtocolVersion: mcpgo.LATEST_PROTOCOL_VERSION,
			ClientInfo:      mcpgo.Implementation{Name: mcpForwardClientName, Version: "1"},
		}}); err != nil {
			// Nothing of the call has been sent: the session never opened.
			return replicaforward.Result{Outcome: replicaforward.Unreachable}, nil
		}
		res, err := cli.CallTool(ctx, mcpgo.CallToolRequest{Params: req.Params})
		if err != nil {
			if !rt.callWritten.Load() {
				return replicaforward.Result{Outcome: replicaforward.Unreachable}, nil
			}
			return replicaforward.Result{}, &replicaforward.SendError{Protocol: cluster.APIProtocolMCP, Target: t.Endpoint, Err: err}
		}
		if out, refused := classifyMCPAnswer(res); refused {
			return out, nil
		}
		result = res
		return replicaforward.Result{Outcome: replicaforward.Served}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, f.cfg.ForwardTimeout())
	defer cancel()
	target, ferr := f.cfg.Locator.Forward(ctx, refusal, cluster.APIProtocolMCP, send)
	if ferr != nil {
		var sendErr *replicaforward.SendError
		if errors.As(ferr, &sendErr) {
			return nil, ferr, true
		}
		return nil, nil, false
	}
	if result.Meta == nil {
		result.Meta = &mcpgo.Meta{}
	}
	if result.Meta.AdditionalFields == nil {
		result.Meta.AdditionalFields = map[string]any{}
	}
	result.Meta.AdditionalFields[replicaforward.MetadataForwardedTo] = strconv.FormatUint(target.ReplicaID, 10)
	return result, nil, true
}

// classifyMCPAnswer reports whether a replica's tool result is a replica
// refusal -- it holds no replica either, or is a follower refusing the
// write. Any other result is the caller's.
func classifyMCPAnswer(res *mcpgo.CallToolResult) (replicaforward.Result, bool) {
	if !res.IsError {
		return replicaforward.Result{}, false
	}
	p, ok := wire.DecodeErrorPayload(res.StructuredContent)
	if !ok {
		return replicaforward.Result{}, false
	}
	switch p.Reason {
	case replicaforward.ReasonNotAReplica:
		return replicaforward.Result{Outcome: replicaforward.NotAReplica}, true
	case replicaforward.ReasonNotLeader:
		return replicaforward.Result{Outcome: replicaforward.NotLeader, LeaderRaftAddr: replicaforward.LeaderRaftAddr(p.Metadata)}, true
	}
	return replicaforward.Result{}, false
}

// onBehalfTransport signs every HTTP request of one forward -- the session's
// initialize, the tool call, the session's close -- with an assertion that
// the caller made it, bound to the request as sent. It notes whether the tool
// call's request was written, which is what tells a call that never left this
// node from one that may have run.
type onBehalfTransport struct {
	base        http.RoundTripper
	caller      *auth.Identity
	key         crypto.Signer
	nodeID      string
	callWritten atomic.Bool
}

// RoundTrip signs req and sends it.
func (t *onBehalfTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		b, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		body = b
	}
	assertion, sig, err := forward.SignCaller(t.caller, forward.HTTPBinding(req.Method, req.URL.RequestURI(), body), t.key, t.nodeID, time.Now())
	if err != nil {
		return nil, err
	}
	ctx := req.Context()
	if isToolCall(body) {
		ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteHeaders: func() { t.callWritten.Store(true) }})
	}
	out := req.Clone(ctx)
	out.Body = io.NopCloser(bytes.NewReader(body))
	out.ContentLength = int64(len(body))
	out.Header.Set(forward.HeaderAssertion, assertion)
	out.Header.Set(forward.HeaderSignature, sig)
	return t.base.RoundTrip(out)
}

// isToolCall reports whether body is a JSON-RPC tools/call request.
func isToolCall(body []byte) bool {
	var msg struct {
		Method string `json:"method"`
	}
	return json.Unmarshal(body, &msg) == nil && msg.Method == string(mcpgo.MethodToolsCall)
}
