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

package rest

import (
	"bytes"
	"context"
	"crypto"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
)

// WithReplicaForwarding makes this listener forward a request it refuses
// because the node holds no replica of the database to a node that does, on
// the caller's behalf (docs/replica-forwarding). Without it the listener
// answers the refusal. The watch stream is never forwarded.
func WithReplicaForwarding(cfg replicaforward.Config) ServerOption {
	return func(s *Server) {
		c := cfg
		s.replicaForwarding = &c
	}
}

// maxHeldRefusal bounds the error body a refusalCatcher holds back while it
// decides whether the answer is a refusal to forward; a refusal is a few
// hundred bytes.
const maxHeldRefusal = 64 << 10

// restStreams are the routes whose answer is a stream. A stream is refused
// with the replica set, never forwarded.
var restStreams = map[string]bool{http.MethodGet + " /watch": true}

// hopByHopHeaders are not relayed between the caller, this node and the
// target (RFC 9110 §7.6.1), nor are the caller's credentials and the
// forwarding controls, which this node sets itself.
var hopByHopHeaders = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Connection": true, "Te": true, "Trailer": true,
	"Transfer-Encoding": true, "Upgrade": true, "Proxy-Authenticate": true, "Proxy-Authorization": true,
	"Authorization":         true,
	forward.HeaderAssertion: true,
	forward.HeaderSignature: true,
	headerForwardedBy:       true,
	headerForwardHops:       true,
	"Content-Length":        true,
}

// restReplicaForwarder forwards a REST request this node refused with
// NotAReplica to a replica, carrying a signed assertion of the caller, and
// relays the replica's answer.
type restReplicaForwarder struct {
	cfg    replicaforward.Config
	key    crypto.Signer
	nodeID string
	client *http.Client
}

// newRESTReplicaForwarder returns the forwarder for a listener serving
// nodeTLS: its certificate is the client certificate a forward presents and
// its key signs the caller assertion.
func newRESTReplicaForwarder(cfg replicaforward.Config, nodeTLS *tls.Config) *restReplicaForwarder {
	key, nodeID := forward.NodeIdentity(nodeTLS)
	half := cfg.ForwardTimeout() / 2
	return &restReplicaForwarder{
		cfg:    cfg,
		key:    key,
		nodeID: nodeID,
		client: &http.Client{Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:              cfg.RootCAs,
				Certificates:         nodeTLS.Certificates,
				GetClientCertificate: nodeTLS.GetClientCertificate,
				MinVersion:           tls.VersionTLS13,
			},
			ForceAttemptHTTP2: true,
			// A connection gets half the forward's budget, so a target that
			// never answers leaves time for the one retry.
			DialContext:         (&net.Dialer{Timeout: half}).DialContext,
			TLSHandshakeTimeout: half,
		}},
	}
}

// close releases the idle connections to targets.
func (f *restReplicaForwarder) close() {
	f.client.CloseIdleConnections()
}

// restForwardedIn reports whether r was forwarded here on a caller's behalf
// -- the hop marker. Such a request is never forwarded again.
func restForwardedIn(r *http.Request) bool {
	return r.Header.Get(forward.HeaderAssertion) != "" && r.Header.Get(forward.HeaderSignature) != ""
}

// middleware forwards what next refuses as a non-replica. It wraps the
// router directly, inside authentication and authorization, so the caller
// has been admitted here before anything is forwarded.
func (f *restReplicaForwarder) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if restForwardedIn(r) || restStreams[r.Method+" "+r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		body := &recordedBody{src: r.Body}
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = body
		}
		c := newRefusalCatcher(w)
		next.ServeHTTP(c, r)
		refusal, ok := c.refusal()
		if !ok {
			c.release()
			return
		}
		payload, ok := body.whole(forward.MaxBodyBytes)
		if !ok {
			c.release()
			return
		}
		f.forward(w, r, payload, refusal, c)
	})
}

// forward forwards r and relays the answer. Every failure to forward ends in
// the refusal c holds, never in a local answer; a failure after the request
// reached a target ends in that failure.
func (f *restReplicaForwarder) forward(w http.ResponseWriter, r *http.Request, payload []byte, refusal *placement.NotAReplicaError, c *refusalCatcher) {
	assertion, sig, err := forward.SignCaller(auth.IdentityFromContext(r.Context()),
		forward.HTTPBinding(r.Method, r.URL.RequestURI(), payload), f.key, f.nodeID, time.Now())
	if err != nil {
		slog.Warn("replicaforward: not forwarded", "protocol", cluster.APIProtocolREST, "tenant", refusal.TenantID, "database", refusal.Database, "error", err)
		c.release()
		return
	}

	var answer *http.Response
	send := func(ctx context.Context, t replicaforward.Target) (replicaforward.Result, error) {
		req, err := http.NewRequestWithContext(ctx, r.Method, "https://"+t.Endpoint+r.URL.RequestURI(), bytes.NewReader(payload))
		if err != nil {
			return replicaforward.Result{Outcome: replicaforward.Unreachable}, nil
		}
		for k, vals := range r.Header {
			if !hopByHopHeaders[http.CanonicalHeaderKey(k)] {
				req.Header[k] = append([]string(nil), vals...)
			}
		}
		req.Header.Set(forward.HeaderAssertion, assertion)
		req.Header.Set(forward.HeaderSignature, sig)
		var wrote atomic.Bool
		req = req.WithContext(httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteHeaders: func() { wrote.Store(true) }}))
		resp, err := f.client.Do(req)
		if err != nil {
			if !wrote.Load() {
				return replicaforward.Result{Outcome: replicaforward.Unreachable}, nil
			}
			return replicaforward.Result{}, &replicaforward.SendError{Protocol: cluster.APIProtocolREST, Target: t.Endpoint, Err: err}
		}
		if res, refused := classifyRESTAnswer(resp); refused {
			return res, nil
		}
		answer = resp
		return replicaforward.Result{Outcome: replicaforward.Served}, nil
	}

	ctx, cancel := context.WithTimeout(r.Context(), f.cfg.ForwardTimeout())
	defer cancel()
	target, ferr := f.cfg.Locator.Forward(ctx, refusal, cluster.APIProtocolREST, send)
	if ferr != nil {
		var sendErr *replicaforward.SendError
		if errors.As(ferr, &sendErr) {
			writeErrorJSON(w, mapHTTPError(ferr), ferr)
			return
		}
		c.release()
		return
	}
	defer func() { _ = answer.Body.Close() }()
	for k, vals := range answer.Header {
		if !hopByHopHeaders[http.CanonicalHeaderKey(k)] {
			w.Header()[k] = vals
		}
	}
	w.Header().Set(replicaforward.MetadataForwardedTo, strconv.FormatUint(target.ReplicaID, 10))
	w.WriteHeader(answer.StatusCode)
	if _, err := io.Copy(w, answer.Body); err != nil {
		slog.Warn("replicaforward: relaying the answer failed", "protocol", cluster.APIProtocolREST, "target", target.Endpoint, "error", err)
	}
}

// classifyRESTAnswer reports whether resp is a replica refusal -- the
// target holds no replica either, or is a follower refusing the write -- and
// closes it when it is. Any other answer is the caller's, left unread.
func classifyRESTAnswer(resp *http.Response) (replicaforward.Result, bool) {
	if resp.StatusCode != http.StatusServiceUnavailable {
		return replicaforward.Result{}, false
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxHeldRefusal))
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil {
		return replicaforward.Result{}, false
	}
	var body errorResponse
	if json.Unmarshal(raw, &body) != nil {
		return replicaforward.Result{}, false
	}
	switch body.Code {
	case replicaforward.ReasonNotAReplica:
		return replicaforward.Result{Outcome: replicaforward.NotAReplica}, true
	case replicaforward.ReasonNotLeader:
		return replicaforward.Result{Outcome: replicaforward.NotLeader, LeaderRaftAddr: replicaforward.LeaderRaftAddr(body.Metadata)}, true
	}
	return replicaforward.Result{}, false
}

// recordedBody keeps the request body the handler reads, so a refused
// request can be forwarded with the body the caller sent.
type recordedBody struct {
	src  io.ReadCloser
	seen bytes.Buffer
	over bool
}

// Read reads from the caller's body, keeping a copy up to MaxBodyBytes.
func (b *recordedBody) Read(p []byte) (int, error) {
	n, err := b.src.Read(p)
	if n > 0 && !b.over {
		if int64(b.seen.Len()+n) > forward.MaxBodyBytes {
			b.over = true
		} else {
			b.seen.Write(p[:n])
		}
	}
	return n, err
}

// Close closes the caller's body.
func (b *recordedBody) Close() error { return b.src.Close() }

// whole returns the whole request body -- what the handler read and what it
// left -- or false when it exceeds limit or cannot be read.
func (b *recordedBody) whole(limit int64) ([]byte, bool) {
	if b.src == nil || b.src == http.NoBody {
		return nil, true
	}
	if b.over {
		return nil, false
	}
	rest, err := io.ReadAll(io.LimitReader(b.src, limit-int64(b.seen.Len())+1))
	if err != nil || int64(b.seen.Len()+len(rest)) > limit {
		return nil, false
	}
	b.seen.Write(rest)
	return b.seen.Bytes(), true
}

// refusalCatcher stands between a handler and the caller's ResponseWriter.
// It passes every answer through unchanged except a JSON 503, which it holds
// back until the handler returns, so a NotAReplica refusal can be forwarded
// instead of answered. Its own header map keeps a held answer's headers off
// the caller's response until it is released.
type refusalCatcher struct {
	w           http.ResponseWriter
	header      http.Header
	status      int
	wroteHeader bool
	held        bool
	buf         bytes.Buffer
}

func newRefusalCatcher(w http.ResponseWriter) *refusalCatcher {
	return &refusalCatcher{w: w, header: w.Header().Clone()}
}

// Header returns the header map the handler writes.
func (c *refusalCatcher) Header() http.Header { return c.header }

// WriteHeader holds a JSON 503 back and passes any other status through.
func (c *refusalCatcher) WriteHeader(status int) {
	if c.wroteHeader {
		return
	}
	c.wroteHeader, c.status = true, status
	if status == http.StatusServiceUnavailable && c.header.Get("Content-Type") == "application/json" {
		c.held = true
		return
	}
	c.passHeader()
}

// Write buffers a held answer and passes any other through. A held answer
// that outgrows a refusal is released.
func (c *refusalCatcher) Write(p []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	if c.held {
		if c.buf.Len()+len(p) <= maxHeldRefusal {
			return c.buf.Write(p)
		}
		c.release()
	}
	return c.w.Write(p)
}

// Flush flushes an answer passed through.
func (c *refusalCatcher) Flush() {
	if f, ok := c.w.(http.Flusher); ok && !c.held {
		f.Flush()
	}
}

// Unwrap returns the caller's ResponseWriter, for http.ResponseController.
func (c *refusalCatcher) Unwrap() http.ResponseWriter { return c.w }

// refusal returns the NotAReplica refusal a held answer carries.
func (c *refusalCatcher) refusal() (*placement.NotAReplicaError, bool) {
	if !c.held {
		return nil, false
	}
	var body errorResponse
	if json.Unmarshal(c.buf.Bytes(), &body) != nil || body.Code != replicaforward.ReasonNotAReplica {
		return nil, false
	}
	refusal, err := replicaforward.ParseNotAReplica(body.Metadata)
	return refusal, err == nil
}

// release writes the handler's answer as it was, if it is still held, and
// any headers the handler set without writing a status.
func (c *refusalCatcher) release() {
	if !c.wroteHeader {
		c.copyHeader()
		return
	}
	if !c.held {
		return
	}
	c.held = false
	c.passHeader()
	_, _ = c.w.Write(c.buf.Bytes())
	c.buf.Reset()
}

// passHeader writes the handler's headers and status to the caller.
func (c *refusalCatcher) passHeader() {
	c.copyHeader()
	c.w.WriteHeader(c.status)
}

// copyHeader makes the caller's header map the handler's.
func (c *refusalCatcher) copyHeader() {
	dst := c.w.Header()
	for k := range dst {
		if _, ok := c.header[k]; !ok {
			delete(dst, k)
		}
	}
	maps.Copy(dst, c.header)
}
