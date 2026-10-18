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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package quic

import (
	"testing"

	quicgo "github.com/quic-go/quic-go"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// fakeAwaitingAdminCredentialProbe is a deterministic double for the
// awaitingAdminCredentialProbe interface, mirroring how sealedWindowProbe is
// substituted in the sealed-window tests.
type fakeAwaitingAdminCredentialProbe struct{ awaiting bool }

func (f *fakeAwaitingAdminCredentialProbe) AwaitingAdminCredential() bool { return f.awaiting }

// roundTripOnBehalf wraps payload for method in a wire.MethodOnBehalf
// envelope -- exactly as a cluster peer forwarding a call on a caller's
// behalf would -- and round-trips it to env's server. assertion and sig are
// never validated by the client config this file uses (the integration TLS
// bundle presents no client certificate, so AuthenticateQUICOnBehalf always
// fails on ErrNoPeerCertificate before it would even look at them); garbage
// strings are enough to prove whether the server attempted to authenticate
// the envelope at all.
func roundTripOnBehalf(t *testing.T, conn *quicgo.Conn, method string, requestID uint64, payload []byte) (status uint16, respPayload []byte) {
	t.Helper()
	env, err := wire.EncodeOnBehalf(wire.OnBehalf{
		Method:    method,
		Assertion: "not-a-real-assertion",
		Signature: "not-a-real-signature",
		Payload:   payload,
	})
	if err != nil {
		t.Fatalf("EncodeOnBehalf: %v", err)
	}
	_, status, respPayload = roundTrip(t, conn, wire.MethodOnBehalf, requestID, env)
	return status, respPayload
}

// TestAwaitingAdminCredential_ForwardedRequest_NonAllowlistedMethodIsRefused
// is the go-qrdb#548 review item 3 / replica-forwarding composition test: a
// cluster peer forwarding kv.Put (not on AwaitingAdminCredentialAllowlist) to
// a node that is awaiting admin credential must NOT reach
// authenticateOnBehalf at all -- the awaiting gate runs first, on the
// envelope's unwrapped inner method, and refuses with
// AwaitingAdminCredentialError. Wrapping a refused method in an OnBehalf
// envelope must never be a way around this gate.
func TestAwaitingAdminCredential_ForwardedRequest_NonAllowlistedMethodIsRefused(t *testing.T) {
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{},
		WithAuthMiddleware(authmw.New(&auth.NoopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))),
		WithAwaitingAdminCredentialProbe(&fakeAwaitingAdminCredentialProbe{awaiting: true}),
	)
	conn := env.dialServer(t)

	status, body := roundTripOnBehalf(t, conn, "kv.Put", 1, []byte("put payload"))

	if status != wire.StatusUnavailable {
		t.Fatalf("status = %s, want StatusUnavailable (%d)", wire.StatusName(status), wire.StatusUnavailable)
	}
	payload, err := wire.DecodeErrorPayload(body)
	if err != nil {
		t.Fatalf("DecodeErrorPayload: %v", err)
	}
	want := (&authmw.AwaitingAdminCredentialError{}).Error()
	if payload.Message != want {
		t.Fatalf("message = %q, want the awaiting-admin-credential refusal %q (forwarded request reached a different gate)",
			payload.Message, want)
	}
}

// TestAwaitingAdminCredential_ForwardedRequest_AllowlistedMethodSkipsAuthToo
// is the other half of the same composition: ceremony.Status IS on
// AwaitingAdminCredentialAllowlist, so a forwarded call to it must be
// admitted exactly as a direct call would be -- skipping authenticateOnBehalf
// entirely, not merely tolerating it. The envelope's assertion and signature
// are garbage and would fail AuthenticateQUICOnBehalf (ErrNoPeerCertificate)
// if it ran; StatusOK proves it never did.
func TestAwaitingAdminCredential_ForwardedRequest_AllowlistedMethodSkipsAuthToo(t *testing.T) {
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{},
		WithAuthMiddleware(authmw.New(&auth.NoopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))),
		WithAwaitingAdminCredentialProbe(&fakeAwaitingAdminCredentialProbe{awaiting: true}),
		WithCeremonyService(&mockCeremonyService{}),
	)
	conn := env.dialServer(t)

	status, body := roundTripOnBehalf(t, conn, "ceremony.Status", 1, nil)

	if status != wire.StatusOK {
		payload, _ := wire.DecodeErrorPayload(body)
		t.Fatalf("status = %s (%q), want StatusOK: an allowlisted method, forwarded, must be admitted while awaiting admin credential",
			wire.StatusName(status), payload.Message)
	}
}

// TestAwaitingAdminCredential_ForwardedRequest_NotAwaitingStillAuthenticates
// is the control: once the node is no longer awaiting admin credential, the
// SAME forwarded, garbage-signed envelope for a non-allowlisted method must
// fail authenticateOnBehalf's real check (StatusUnauthenticated), not the
// awaiting gate's. This is what proves the first test's refusal comes from
// the awaiting gate specifically, and that merging in main's onBehalf path
// left it reachable and enforced once the gate no longer applies.
func TestAwaitingAdminCredential_ForwardedRequest_NotAwaitingStillAuthenticates(t *testing.T) {
	env := startIntegrationServer(t, &mockKVService{}, &mockAdminService{},
		WithAuthMiddleware(authmw.New(&auth.NoopAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))),
		WithAwaitingAdminCredentialProbe(&fakeAwaitingAdminCredentialProbe{awaiting: false}),
	)
	conn := env.dialServer(t)

	status, _ := roundTripOnBehalf(t, conn, "kv.Put", 1, []byte("put payload"))

	if status != wire.StatusUnauthenticated {
		t.Fatalf("status = %s, want StatusUnauthenticated: a forwarded request with no peer certificate must fail real auth once the node is not awaiting",
			wire.StatusName(status))
	}
}
