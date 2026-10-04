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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward/replicaforwardtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// restSeen is what a fake target saw of one forwarded request.
type restSeen struct {
	subject, database, body string
	authorization           string
	verifyErr               error
}

// restTarget is a replica's REST listener. It verifies the on-behalf
// assertion as the auth middleware binds it -- method, request URI and body,
// presented by a cluster peer -- and answers with answer.
type restTarget struct {
	addr     string
	raftAddr string
	calls    atomic.Int32
	answer   func(w http.ResponseWriter, r *http.Request) bool

	mu   sync.Mutex
	seen []restSeen
}

func startRESTTarget(t *testing.T, ca *replicaforwardtest.CA, nodeID, raftAddr string) *restTarget {
	t.Helper()
	rt := &restTarget{raftAddr: raftAddr}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rt.calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		seen := restSeen{database: r.Header.Get("X-QRDB-Database"), body: string(body), authorization: r.Header.Get("Authorization")}
		a, err := forward.VerifyBinding(r.Header.Get(forward.HeaderAssertion), r.Header.Get(forward.HeaderSignature),
			r.TLS.PeerCertificates[0], "", time.Now(), forward.HTTPBinding(r.Method, r.URL.RequestURI(), body))
		seen.verifyErr = err
		if a != nil {
			seen.subject = a.Subject
		}
		rt.mu.Lock()
		rt.seen = append(rt.seen, seen)
		rt.mu.Unlock()
		if err != nil {
			writeErrorJSON(w, http.StatusUnauthorized, err)
			return
		}
		if rt.answer != nil && rt.answer(w, r) {
			return
		}
		w.Header().Set("X-Target-Says", "hello")
		writeJSON(w, map[string]string{"served_by": nodeID, "body": string(body)})
	}))
	srv.TLS = ca.TLS(t, nodeID)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	rt.addr = strings.TrimPrefix(srv.URL, "https://")
	return rt
}

func (rt *restTarget) lastSeen(t *testing.T) restSeen {
	t.Helper()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.seen) == 0 {
		t.Fatal("the target saw no request")
	}
	return rt.seen[len(rt.seen)-1]
}

// restFwdCluster is an ingress node holding no replica, in front of the
// replicas the placement names.
type restFwdCluster struct {
	cache     *replicaforward.LeaderCache
	forwarder *restReplicaForwarder
	url       string
	client    *http.Client
	handled   atomic.Int32
}

// newRESTFwdCluster serves the ingress node. Its handler reads part of the
// body -- as a handler does before resolving its service -- then refuses.
func newRESTFwdCluster(t *testing.T, ca *replicaforwardtest.CA, timeout time.Duration, targets ...*restTarget) *restFwdCluster {
	t.Helper()
	c := &restFwdCluster{cache: replicaforward.NewLeaderCache()}
	refusal := &placement.NotAReplicaError{TenantID: "acme", Database: "orders", ShardID: 42, NodeID: "node-a", LeaderReplicaID: 1}
	var members replicaforwardtest.Members
	for i, rt := range targets {
		refusal.Replicas = append(refusal.Replicas, placement.Replica{ReplicaID: uint64(i + 1), NodeID: "node-" + strconv.Itoa(i+1), RaftAddr: rt.raftAddr})
		members = append(members, cluster.Member{NodeID: uint64(i + 1), Address: rt.raftAddr,
			APIEndpoints: map[cluster.APIProtocol]string{cluster.APIProtocolREST: rt.addr}})
	}
	ingressTLS := ca.TLS(t, "node-a")
	c.forwarder = newRESTReplicaForwarder(replicaforward.Config{
		Locator: replicaforward.NewLocator(members, c.cache, "raft-a", nil), RootCAs: ca.Pool, Timeout: timeout,
	}, ingressTLS)
	t.Cleanup(c.forwarder.close)

	refusing := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.handled.Add(1)
		if r.Body != nil {
			_, _ = io.ReadFull(r.Body, make([]byte, 3))
		}
		writeErrorJSON(w, http.StatusServiceUnavailable, refusal)
	})
	authn := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Test-Anonymous") == "" {
				r = r.WithContext(auth.WithIdentity(r.Context(), &auth.Identity{Subject: "alice", TenantID: "acme"}))
			}
			next.ServeHTTP(w, r)
		})
	}
	srv := httptest.NewUnstartedServer(authn(c.forwarder.middleware(refusing)))
	srv.TLS = ingressTLS
	srv.StartTLS()
	t.Cleanup(srv.Close)
	c.url = srv.URL
	c.client = &http.Client{Transport: &http.Transport{TLSClientConfig: ca.TLS(t, "")}}
	return c
}

// restAnswer is an answer the ingress node gave, read whole.
type restAnswer struct {
	status int
	header http.Header
	body   string
}

// answerOf reads resp's body; the caller closes it.
func answerOf(t *testing.T, resp *http.Response) restAnswer {
	t.Helper()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return restAnswer{status: resp.StatusCode, header: resp.Header, body: string(b)}
}

func (c *restFwdCluster) put(t *testing.T, body string, header ...string) restAnswer {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, c.url+"/kv/k1?consistency=strong", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-QRDB-Database", "orders")
	req.Header.Set("Authorization", "Bearer secret")
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := c.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	return answerOf(t, resp)
}

// refusalBody decodes resp as the ingress node's NotAReplica refusal.
func refusalBody(t *testing.T, a restAnswer) *placement.NotAReplicaError {
	t.Helper()
	var body errorResponse
	if err := json.Unmarshal([]byte(a.body), &body); err != nil || a.status != http.StatusServiceUnavailable || body.Code != replicaforward.ReasonNotAReplica {
		t.Fatalf("answer %d %+v (%v); want the NOT_A_REPLICA refusal", a.status, body, err)
	}
	refusal, err := replicaforward.ParseNotAReplica(body.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	return refusal
}

func TestReplicaForwardREST_ForwardsAsTheCallerAndRelaysTheAnswer(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	b := startRESTTarget(t, ca, "node-1", "raft-b")
	c := newRESTFwdCluster(t, ca, 5*time.Second, b)

	resp := c.put(t, `{"value":"hello"}`)
	if resp.status != http.StatusOK || !strings.Contains(resp.body, `"served_by":"node-1"`) {
		t.Fatalf("answer %d %s; want the replica's", resp.status, resp.body)
	}
	if resp.header.Get("X-Target-Says") != "hello" || resp.header.Get(replicaforward.MetadataForwardedTo) != "1" {
		t.Fatalf("headers %v; want the replica's and %s: 1", resp.header, replicaforward.MetadataForwardedTo)
	}
	seen := b.lastSeen(t)
	if seen.verifyErr != nil || seen.subject != "alice" || seen.body != `{"value":"hello"}` {
		t.Fatalf("target saw %+v; want alice's whole body under a verified assertion", seen)
	}
	if seen.database != "orders" || seen.authorization != "" {
		t.Fatalf("target saw database %q, authorization %q; want the scope carried and the credential dropped", seen.database, seen.authorization)
	}
	if l, ok := c.cache.Get(42); !ok || l.RaftAddr != "raft-b" {
		t.Fatalf("cache = %+v, %v; want the replica that served", l, ok)
	}
}

func TestReplicaForwardREST_AForwardedRequestIsNeverForwardedAgain(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	b := startRESTTarget(t, ca, "node-1", "raft-b")
	c := newRESTFwdCluster(t, ca, 5*time.Second, b)

	resp := c.put(t, "v", forward.HeaderAssertion, "a", forward.HeaderSignature, "s")
	if refusalBody(t, resp); b.calls.Load() != 0 {
		t.Fatalf("a forwarded request was forwarded again (%d target calls)", b.calls.Load())
	}
}

func TestReplicaForwardREST_NotLeaderRetriesTheNamedLeader(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	b := startRESTTarget(t, ca, "node-1", "raft-b")
	cc := startRESTTarget(t, ca, "node-2", "raft-c")
	b.answer = func(w http.ResponseWriter, _ *http.Request) bool {
		writeErrorJSON(w, http.StatusServiceUnavailable, &service.NotLeaderError{ShardID: 42, LeaderID: 2, LeaderAddress: "raft-c"})
		return true
	}
	c := newRESTFwdCluster(t, ca, 5*time.Second, b, cc)

	resp := c.put(t, "v")
	if resp.status != http.StatusOK || resp.header.Get(replicaforward.MetadataForwardedTo) != "2" {
		t.Fatalf("answer %d (%s); want the named leader, replica 2", resp.status, resp.body)
	}
	if b.calls.Load() != 1 || cc.calls.Load() != 1 {
		t.Fatalf("calls b=%d c=%d; want one each", b.calls.Load(), cc.calls.Load())
	}
}

func TestReplicaForwardREST_UnreachableRetriesTheNextReplica(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	dead := &restTarget{raftAddr: "raft-b", addr: replicaforwardtest.ClosedAddr(t)}
	cc := startRESTTarget(t, ca, "node-2", "raft-c")
	c := newRESTFwdCluster(t, ca, 5*time.Second, dead, cc)

	if resp := c.put(t, "v"); resp.status != http.StatusOK || cc.calls.Load() != 1 {
		t.Fatalf("answer %d after %d calls to the next replica; want it served there", resp.status, cc.calls.Load())
	}
}

func TestReplicaForwardREST_AFailureAfterSendingIsNotRetried(t *testing.T) {
	for name, answer := range map[string]func(w http.ResponseWriter, r *http.Request) bool{
		"connection dropped": func(w http.ResponseWriter, _ *http.Request) bool {
			conn, _, err := http.NewResponseController(w).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return true
		},
		"timeout": func(_ http.ResponseWriter, r *http.Request) bool {
			<-r.Context().Done()
			return true
		},
	} {
		t.Run(name, func(t *testing.T) {
			ca := replicaforwardtest.NewCA(t)
			b := startRESTTarget(t, ca, "node-1", "raft-b")
			cc := startRESTTarget(t, ca, "node-2", "raft-c")
			b.answer = answer
			c := newRESTFwdCluster(t, ca, 300*time.Millisecond, b, cc)

			resp := c.put(t, "v")
			var body errorResponse
			_ = json.Unmarshal([]byte(resp.body), &body)
			if resp.status < 500 || body.Code == replicaforward.ReasonNotAReplica {
				t.Fatalf("answer %d %+v; want the failure, not the refusal", resp.status, body)
			}
			if cc.calls.Load() != 0 {
				t.Fatalf("a request that may have applied was sent again (%d)", cc.calls.Load())
			}
		})
	}
}

func TestReplicaForwardREST_TheTargetsErrorIsTheAnswer(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	b := startRESTTarget(t, ca, "node-1", "raft-b")
	b.answer = func(w http.ResponseWriter, _ *http.Request) bool {
		writeErrorJSON(w, http.StatusNotFound, &service.KeyNotFoundError{Key: "k1"})
		return true
	}
	c := newRESTFwdCluster(t, ca, 5*time.Second, b)
	if resp := c.put(t, "v"); resp.status != http.StatusNotFound {
		t.Fatalf("answer %d; want the replica's 404", resp.status)
	}
}

func TestReplicaForwardREST_RefusesWhatItCannotForward(t *testing.T) {
	for name, tc := range map[string]struct {
		header []string
		answer func(w http.ResponseWriter, r *http.Request) bool
		body   string
	}{
		"no authenticated caller": {header: []string{"X-Test-Anonymous", "1"}, body: "v"},
		"every replica refuses": {body: "v", answer: func(w http.ResponseWriter, _ *http.Request) bool {
			writeErrorJSON(w, http.StatusServiceUnavailable, &placement.NotAReplicaError{TenantID: "acme", Database: "orders", NodeID: "node-x"})
			return true
		}},
		"a body larger than a forward carries": {body: strings.Repeat("x", int(forward.MaxBodyBytes)+1)},
	} {
		t.Run(name, func(t *testing.T) {
			ca := replicaforwardtest.NewCA(t)
			b := startRESTTarget(t, ca, "node-1", "raft-b")
			cc := startRESTTarget(t, ca, "node-2", "raft-c")
			b.answer, cc.answer = tc.answer, tc.answer
			c := newRESTFwdCluster(t, ca, 5*time.Second, b, cc)

			if got := refusalBody(t, c.put(t, tc.body, tc.header...)); got.NodeID != "node-a" || len(got.Replicas) != 2 {
				t.Fatalf("refusal = %+v; want the ingress node's, with its replica set", got)
			}
		})
	}
}

// TestReplicaForwardREST_TheWatchStreamIsNotForwarded: a stream refuses.
func TestReplicaForwardREST_TheWatchStreamIsNotForwarded(t *testing.T) {
	ca := replicaforwardtest.NewCA(t)
	b := startRESTTarget(t, ca, "node-1", "raft-b")
	c := newRESTFwdCluster(t, ca, 5*time.Second, b)
	resp, err := c.client.Get(c.url + "/watch?key=k")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if refusalBody(t, answerOf(t, resp)); b.calls.Load() != 0 {
		t.Fatal("the watch stream was forwarded")
	}
}

// TestRefusalCatcher_PassesEveryOtherAnswerThrough: success, a 503 that is
// not a refusal, a handler that writes nothing, and a streamed answer reach
// the caller as written.
func TestRefusalCatcher_PassesEveryOtherAnswerThrough(t *testing.T) {
	for name, tc := range map[string]struct {
		handler func(w http.ResponseWriter)
		status  int
		body    string
	}{
		"success": {handler: func(w http.ResponseWriter) {
			w.Header().Set("X-A", "1")
			_, _ = w.Write([]byte("ok"))
		}, status: http.StatusOK, body: "ok"},
		"another 503": {handler: func(w http.ResponseWriter) {
			writeErrorJSON(w, http.StatusServiceUnavailable, &service.SubsystemClosedError{})
		}, status: http.StatusServiceUnavailable},
		"nothing written": {handler: func(w http.ResponseWriter) { w.Header().Set("X-A", "1") }, status: http.StatusOK},
		"an oversized 503": {handler: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write(bytes.Repeat([]byte("x"), maxHeldRefusal+1))
		}, status: http.StatusServiceUnavailable},
		"streamed": {handler: func(w http.ResponseWriter) {
			_, _ = w.Write([]byte("a"))
			w.(http.Flusher).Flush()
			_, _ = w.Write([]byte("b"))
		}, status: http.StatusOK, body: "ab"},
	} {
		rec := httptest.NewRecorder()
		c := newRefusalCatcher(rec)
		tc.handler(c)
		if _, ok := c.refusal(); ok {
			t.Fatalf("%s: read as a refusal", name)
		}
		c.release()
		if rec.Code != tc.status || (tc.body != "" && rec.Body.String() != tc.body) {
			t.Errorf("%s: caller got %d %q; want %d %q", name, rec.Code, rec.Body.String(), tc.status, tc.body)
		}
		if name == "success" || name == "nothing written" {
			if rec.Header().Get("X-A") != "1" {
				t.Errorf("%s: the handler's header was lost", name)
			}
		}
	}
	if (&refusalCatcher{}).Unwrap() != nil {
		t.Fatal("Unwrap of an empty catcher")
	}
}

// TestRecordedBody_WholeIsWhatTheCallerSent: what the handler read and what
// it left, together, and nothing beyond the limit.
func TestRecordedBody_WholeIsWhatTheCallerSent(t *testing.T) {
	b := &recordedBody{src: io.NopCloser(strings.NewReader("abcdef"))}
	_, _ = io.ReadFull(b, make([]byte, 2))
	if got, ok := b.whole(10); !ok || string(got) != "abcdef" {
		t.Fatalf("whole = %q, %v", got, ok)
	}
	b = &recordedBody{src: io.NopCloser(strings.NewReader("abcdef"))}
	if _, ok := b.whole(3); ok {
		t.Fatal("a body over the limit was whole")
	}
	if got, ok := (&recordedBody{src: http.NoBody}).whole(3); !ok || got != nil {
		t.Fatalf("no body = %q, %v", got, ok)
	}
	if err := (&recordedBody{src: io.NopCloser(strings.NewReader(""))}).Close(); err != nil {
		t.Fatal(err)
	}
}
