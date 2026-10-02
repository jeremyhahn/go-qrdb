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

package middleware

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// signedAssertionRequest signs the assertion fields as the forwarding node
// does -- the canonical JSON bytes, SHA-256, ECDSA -- and builds the request
// presenting them. Fields is a map so a test states the wire exactly.
func signedAssertionRequest(t *testing.T, peerCert *x509.Certificate, key *ecdsa.PrivateKey, fields map[string]any, body []byte, method, target string) *http.Request {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	digest := sha256.Sum256(raw)
	sig, err := key.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	r.Header.Set(forward.HeaderAssertion, base64.StdEncoding.EncodeToString(raw))
	r.Header.Set(forward.HeaderSignature, base64.StdEncoding.EncodeToString(sig))
	r.TLS = &tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{peerCert}}}
	return r
}

func holderAssertionFields(profile string, body []byte, method, target string) map[string]any {
	now := time.Now()
	fields := map[string]any{
		"subject":           "admin",
		"tenant_id":         "system",
		"method":            method,
		"path":              target,
		"body_sha256":       forward.BodyDigest(body),
		"forwarder_node_id": obNodeID,
		"issued_at_unix":    now.Unix(),
		"expires_at_unix":   now.Add(forward.DefaultTTL).Unix(),
	}
	if profile != "" {
		fields["holder_profile"] = profile
	}
	return fields
}

// TestOnBehalf_ForwardedHolderGetsNoRoles: a holder certificate named "admin"
// that reached a follower is forwarded to the leader. The forwarded identity
// must stay a holder -- never resolved to the system admin's account and its
// roles -- exactly as it is on the node it authenticated at.
func TestOnBehalf_ForwardedHolderGetsNoRoles(t *testing.T) {
	nodeCert, nodeKey := obNodeCert(t, obNodeID)
	body := []byte(`{"value":"x"}`)
	r := signedAssertionRequest(t, nodeCert, nodeKey, holderAssertionFields("custodian", body, http.MethodPut, "/kv/foo"), body, http.MethodPut, "/kv/foo")

	admin := &user.User{Username: "admin", TenantID: "system", Lifecycle: user.LifecycleActive, RoleIDs: []string{"admin-role"}}
	_, _, seen := runOnBehalf(t, r, admin, &capturingAuthorizer{})
	if seen == nil {
		t.Fatal("the forwarded holder was refused outright; it must authenticate as a holder")
	}
	if len(seen.Roles) != 0 {
		t.Fatalf("a forwarded holder certificate named admin resolved to admin's roles %v", seen.Roles)
	}
	if seen.Metadata["holder_profile"] != "custodian" {
		t.Fatalf("forwarded identity lost its holder mark: %+v", seen.Metadata)
	}
}

// TestOnBehalf_UnknownHolderProfileIsRejected: a holder profile the leader
// does not recognise is not silently dropped -- dropping it would resolve the
// subject to a user account -- so the assertion is rejected.
func TestOnBehalf_UnknownHolderProfileIsRejected(t *testing.T) {
	nodeCert, nodeKey := obNodeCert(t, obNodeID)
	body := []byte(`{"value":"x"}`)
	r := signedAssertionRequest(t, nodeCert, nodeKey, holderAssertionFields("not-a-profile", body, http.MethodPut, "/kv/foo"), body, http.MethodPut, "/kv/foo")

	admin := &user.User{Username: "admin", TenantID: "system", Lifecycle: user.LifecycleActive, RoleIDs: []string{"admin-role"}}
	rec, ran, _ := runOnBehalf(t, r, admin, &capturingAuthorizer{})
	if ran {
		t.Fatal("an assertion carrying an unknown holder profile was admitted")
	}
	// The same opaque refusal every other rejected assertion gets.
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401/403", rec.Code)
	}
}

// TestOnBehalf_UserForwardStillResolvesRoles is the control: without a holder
// profile the same subject is the user, as before.
func TestOnBehalf_UserForwardStillResolvesRoles(t *testing.T) {
	nodeCert, nodeKey := obNodeCert(t, obNodeID)
	body := []byte(`{"value":"x"}`)
	r := signedAssertionRequest(t, nodeCert, nodeKey, holderAssertionFields("", body, http.MethodPut, "/kv/foo"), body, http.MethodPut, "/kv/foo")

	admin := &user.User{Username: "admin", TenantID: "system", Lifecycle: user.LifecycleActive, RoleIDs: []string{"admin-role"}}
	_, _, seen := runOnBehalf(t, r, admin, &capturingAuthorizer{})
	if seen == nil || len(seen.Roles) != 1 {
		t.Fatalf("user forward identity = %+v, want admin's role", seen)
	}
}
