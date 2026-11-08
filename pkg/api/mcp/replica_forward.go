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
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
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
	connect := cfg.ConnectTimeout()
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
			DialContext:         (&net.Dialer{Timeout: connect}).DialContext,
			TLSHandshakeTimeout: connect,
		},
	}
}

// close releases the idle connections to targets.
func (f *replicaForwarder) close() { f.base.CloseIdleConnections() }

// middleware forwards a tool call next refused as a non-replica, or as not
// the leader, to the node that can serve it. It sits
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
			if refusal, ok := errors.AsType[*placement.NotAReplicaError](err); ok {
				if fwd, ferr, forwarded := f.forward(ctx, req, refusal); forwarded {
					return fwd, ferr
				}
				return res, err
			}
			// What only a shard's leader can commit -- a membership
			// change, a write, the admin-credential step of
			// qrdb_ceremony_initialize
			// (service.CeremonyServiceWithLeaderGate) -- a follower
			// refuses as not the leader. It forwards the identical call to
			// the leader that refusal names through the SAME session
			// machinery forward uses above, rather than answering its own
			// refusal (node parity, owner ruling 2026-10-06).
			if leaderAddr, ok := service.LeaderAddress(err); ok {
				if fwd, ferr, forwarded := f.forwardToLeader(ctx, req, leaderAddr); forwarded {
					return fwd, ferr
				}
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
		var res *mcpgo.CallToolResult
		var err error
		reached := replicaforward.Reconnect(ctx, f.cfg.ConnectTimeout(), func() bool {
			var sent bool
			res, sent, err = f.call(ctx, t.Endpoint, caller, req)
			return sent
		})
		if !reached {
			return replicaforward.Result{Outcome: replicaforward.Unreachable}, nil
		}
		if err != nil {
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
		var ownerErr *database.BindingOwnerUnavailableError
		if errors.As(ferr, &sendErr) || errors.As(ferr, &ownerErr) {
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

// call opens a session with endpoint and makes req on it for caller. sent
// reports whether any of the call left this node; when it did not, the
// session never opened or the call's headers were never written, and the
// call may be made again.
func (f *replicaForwarder) call(ctx context.Context, endpoint string, caller *auth.Identity, req mcpgo.CallToolRequest) (res *mcpgo.CallToolResult, sent bool, err error) {
	rt := &onBehalfTransport{base: f.base, caller: caller, key: f.key, nodeID: f.nodeID}
	cli := mcpclient.NewClient(newForwardTransport("https://"+endpoint+"/mcp", rt))
	defer func() { _ = cli.Close() }()
	if err := cli.Start(ctx); err != nil {
		return nil, false, err
	}
	if _, err := cli.Initialize(ctx, mcpgo.InitializeRequest{Params: mcpgo.InitializeParams{
		ProtocolVersion: mcpgo.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcpgo.Implementation{Name: mcpForwardClientName, Version: "1"},
	}}); err != nil {
		return nil, false, err
	}
	res, err = cli.CallTool(ctx, mcpgo.CallToolRequest{Params: req.Params})
	if err != nil {
		return nil, rt.callWritten.Load(), err
	}
	return res, true, nil
}

// forwardToLeader sends req, unchanged, to the leader at leaderAddr of the
// shard that refused it. Unlike forward it is not a placement-based retry
// across a replica set -- a leader is one node, not a set to retry across --
// it is the single hop a leader-only write's own refusal (see
// service.LeaderAddress) already names by address. It resolves that address through the SAME member records
// forward's Locator reads, opens an MCP session to the leader's MCP
// endpoint exactly as forward does, and relays its result. forwarded is
// false whenever the call was not sent (no resolvable leader, or a
// transport failure before any request reached it), and the caller falls
// back to its own refusal.
func (f *replicaForwarder) forwardToLeader(ctx context.Context, req mcpgo.CallToolRequest, leaderAddr string) (*mcpgo.CallToolResult, error, bool) {
	if leaderAddr == "" {
		slog.Warn("admin forwarding: refusal names no leader")
		return nil, nil, false
	}
	caller := auth.IdentityFromContext(ctx)
	if _, _, err := forward.SignCaller(caller, forward.HTTPBinding(http.MethodPost, "/mcp", nil), f.key, f.nodeID, time.Now()); err != nil {
		slog.Warn("admin forwarding: not forwarded", "protocol", cluster.APIProtocolMCP, "leader_raft_addr", leaderAddr, "error", err)
		return nil, nil, false
	}
	// The deadline covers the leader lookup too: it reads the member
	// records from the system shard, and a read that does not complete
	// would otherwise hold this call open for as long as the caller waits.
	ctx, cancel := context.WithTimeout(ctx, f.cfg.ForwardTimeout())
	defer cancel()
	member, found, merr := f.cfg.Locator.MemberByAddress(ctx, leaderAddr)
	if merr != nil || !found {
		slog.Warn("admin forwarding: leader not found in member records", "leader_raft_addr", leaderAddr, "error", merr)
		return nil, nil, false
	}
	endpoint, ok := member.APIEndpoint(cluster.APIProtocolMCP)
	if !ok || endpoint == "" {
		slog.Warn("admin forwarding: leader advertises no MCP endpoint", "leader_raft_addr", leaderAddr)
		return nil, nil, false
	}

	rt := &onBehalfTransport{base: f.base, caller: caller, key: f.key, nodeID: f.nodeID}
	cli := mcpclient.NewClient(newForwardTransport("https://"+endpoint+"/mcp", rt))
	defer func() { _ = cli.Close() }()
	if err := cli.Start(ctx); err != nil {
		slog.Warn("admin forwarding: could not connect to the leader", "leader_raft_addr", leaderAddr, "endpoint", endpoint, "error", err)
		return nil, nil, false
	}
	if _, err := cli.Initialize(ctx, mcpgo.InitializeRequest{Params: mcpgo.InitializeParams{
		ProtocolVersion: mcpgo.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcpgo.Implementation{Name: mcpForwardClientName, Version: "1"},
	}}); err != nil {
		slog.Warn("admin forwarding: could not open a session with the leader", "leader_raft_addr", leaderAddr, "endpoint", endpoint, "error", err)
		return nil, nil, false
	}
	res, err := cli.CallTool(ctx, mcpgo.CallToolRequest{Params: req.Params})
	if err != nil {
		slog.Warn("admin forwarding: the leader's tool call failed", "leader_raft_addr", leaderAddr, "endpoint", endpoint, "error", err)
		return nil, nil, false
	}
	return res, nil, true
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
