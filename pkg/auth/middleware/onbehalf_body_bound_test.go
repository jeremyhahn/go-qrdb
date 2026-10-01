// Copyright (c) 2026 Jeremy Hahn
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

package middleware

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// countingBody is a request body of zero bytes that counts how many
// bytes the server pulled from it, so a test can see how much of a hostile
// body was read before the request was refused.
type countingBody struct {
	remaining int64
	read      atomic.Int64
}

func (b *countingBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, io.EOF
	}
	n := int64(len(p))
	if n > b.remaining {
		n = b.remaining
	}
	clear(p[:n])
	b.remaining -= n
	b.read.Add(n)
	return int(n), nil
}

func (b *countingBody) Close() error { return nil }

// hostileOnBehalfRequest is PUT /kv/foo from peerCert, carrying the on-behalf
// headers of a small signed assertion and a body of size bytes.
func hostileOnBehalfRequest(t *testing.T, peerCert *x509.Certificate, signKey *ecdsa.PrivateKey, size int64) (*http.Request, *countingBody) {
	t.Helper()
	signed := buildOnBehalfRequest(t, peerCert, signKey, "alice", "acme", obNodeID, time.Now(), []byte("x"), http.MethodPut, "/kv/foo")
	body := &countingBody{remaining: size}
	r := httptest.NewRequest(http.MethodPut, "/kv/foo", nil)
	r.Body = body
	r.ContentLength = size
	r.Header.Set(forward.HeaderAssertion, signed.Header.Get(forward.HeaderAssertion))
	r.Header.Set(forward.HeaderSignature, signed.Header.Get(forward.HeaderSignature))
	r.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{peerCert}}}
	return r, body
}

// hostileBodyBytes is far above any body a node forwards, so a server that
// reads it all has read without a bound.
const hostileBodyBytes = int64(64 << 20)

// TestOnBehalf_NonClusterPeerBodyNeverRead: a certificate that chains to the
// cluster CA but is not a cluster node's -- a user certificate -- can never
// carry an honored assertion, so presenting the on-behalf headers must not
// make the node read the body at all. It used to io.ReadAll the whole body,
// unbounded, before discovering the presenter was not a node.
func TestOnBehalf_NonClusterPeerBodyNeverRead(t *testing.T) {
	userC, userKey := obUserCert(t)
	r, body := hostileOnBehalfRequest(t, userC, userKey, hostileBodyBytes)

	rec, ran, _ := runOnBehalf(t, r, &user.User{Username: "alice", TenantID: "acme", Lifecycle: user.LifecycleActive}, &capturingAuthorizer{})
	if ran {
		t.Fatal("handler ran for a non-cluster presenter")
	}
	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 401/403", rec.Code)
	}
	if n := body.read.Load(); n != 0 {
		t.Fatalf("read %d bytes of a non-cluster presenter's body before refusing it; want 0", n)
	}
}

// TestOnBehalf_ClusterPeerBodyBounded: even a cluster node's body is read
// under a bound -- the largest body a node ever forwards -- before the
// assertion's body binding is checked. It used to be read without one.
func TestOnBehalf_ClusterPeerBodyBounded(t *testing.T) {
	nodeC, nodeKey := obNodeCert(t, obNodeID)
	r, body := hostileOnBehalfRequest(t, nodeC, nodeKey, hostileBodyBytes)

	rec, ran, _ := runOnBehalf(t, r, &user.User{Username: "alice", TenantID: "acme", Lifecycle: user.LifecycleActive}, &capturingAuthorizer{})
	if ran {
		t.Fatal("handler ran for an oversized forwarded body")
	}
	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden && rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want a refusal", rec.Code)
	}
	if n := body.read.Load(); n >= hostileBodyBytes {
		t.Fatalf("read the whole %d-byte forwarded body before verifying its binding; want a bounded read", n)
	}
}
