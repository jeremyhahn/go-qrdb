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

package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// peerThumbprint attaches a verified client certificate to r and returns
// the thumbprint the handler must derive from it.
func peerThumbprint(r *http.Request) string {
	injectFakeTLS(r)
	return mtls.Thumbprint(r.TLS.PeerCertificates[0].Raw)
}

// holderPost drives one POST through the dispatcher.
func holderPost(srv *Server, path, body string, tls bool) (*httptest.ResponseRecorder, string) {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	thumb := ""
	if tls {
		thumb = peerThumbprint(req)
	}
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	return rec, thumb
}

// ---------------------------------------------------------------------------
// System barrier holders
// ---------------------------------------------------------------------------

func TestBarrierHolders_Register_RoutesEachDomain(t *testing.T) {
	for path, want := range map[string]seal.QuorumDomain{
		"/v1/barrier/officers":          seal.DomainUnseal,
		"/v1/barrier/recovery/officers": seal.DomainRecovery,
	} {
		t.Run(path, func(t *testing.T) {
			svc := &mockBarrierService{
				officerRegisterFn: func(_ context.Context, domain seal.QuorumDomain, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
					if domain != want {
						t.Fatalf("domain = %q, want %q", domain, want)
					}
					if req.Username != "carol" || req.Role != seal.RoleCustodian || string(req.CSRPEM) != "csr" {
						t.Fatalf("request = %+v", req)
					}
					if req.Designated != "fido2" || len(req.Factors) != 1 || req.Factors[0].Name != "fido2" {
						t.Fatalf("factors = %+v designated = %q", req.Factors, req.Designated)
					}
					return &service.BarrierOfficerRegisterResult{CertPEM: []byte("cert"), Role: seal.RoleCustodian, Designated: "fido2", Factors: []string{"fido2"}}, nil
				},
			}
			srv := newBarrierTestServer(t, svc)
			rec, _ := holderPost(srv, path, `{"username":"carol","role":"custodian","csr_pem":"csr","designated":"fido2","factors":[{"name":"fido2","params":{"k":"v"}}]}`, false)

			assertStatus(t, rec, http.StatusOK)
			var resp transport.OfficerRegisterResponse
			decodeJSON(t, rec, &resp)
			if resp.CertPEM != "cert" || resp.Role != "custodian" || resp.Designated != "fido2" {
				t.Fatalf("response = %+v", resp)
			}
		})
	}
}

func TestBarrierHolders_Register_Refusals(t *testing.T) {
	svc := &mockBarrierService{
		officerRegisterFn: func(context.Context, seal.QuorumDomain, *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
			return nil, ceremony.ErrSelfEnrolment
		},
	}
	srv := newBarrierTestServer(t, svc)

	rec, _ := holderPost(srv, "/v1/barrier/officers", `{bad`, false)
	assertStatus(t, rec, http.StatusBadRequest)

	rec, _ = holderPost(srv, "/v1/barrier/officers", `{"username":"me","csr_pem":"csr"}`, false)
	assertStatus(t, rec, http.StatusForbidden)
}

func TestBarrierHolders_List(t *testing.T) {
	claimed := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	svc := &mockBarrierService{
		officerListFn: func(_ context.Context, domain seal.QuorumDomain) ([]*service.BarrierOfficerStatus, error) {
			if domain != seal.DomainUnseal {
				t.Fatalf("domain = %q", domain)
			}
			return []*service.BarrierOfficerStatus{{Username: "alice", Role: seal.RoleSecurityOfficer, ShareIndex: 1, ClaimedAt: claimed}}, nil
		},
	}
	srv := newBarrierTestServer(t, svc)
	req := httptest.NewRequest(http.MethodGet, "/v1/barrier/officers", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)

	assertStatus(t, rec, http.StatusOK)
	var resp struct {
		Officers []transport.OfficerStatus `json:"officers"`
	}
	decodeJSON(t, rec, &resp)
	if len(resp.Officers) != 1 || resp.Officers[0].Username != "alice" || resp.Officers[0].Role != "so" || !resp.Officers[0].ClaimedAt.Equal(claimed) {
		t.Fatalf("officers = %+v", resp.Officers)
	}
}

func TestBarrierHolders_List_ServiceError(t *testing.T) {
	svc := &mockBarrierService{
		officerListFn: func(context.Context, seal.QuorumDomain) ([]*service.BarrierOfficerStatus, error) {
			return nil, service.ErrCeremonyNotConfigured
		},
	}
	srv := newBarrierTestServer(t, svc)
	req := httptest.NewRequest(http.MethodGet, "/v1/barrier/officers", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusUnprocessableEntity)
}

// TestBarrierHolders_Claim_TakesThumbprintFromTLS proves both claim steps
// bind to the certificate on the connection, in the domain the path names.
func TestBarrierHolders_Claim_TakesThumbprintFromTLS(t *testing.T) {
	for prefix, want := range map[string]seal.QuorumDomain{
		"/v1/barrier/officers/":          seal.DomainUnseal,
		"/v1/barrier/recovery/officers/": seal.DomainRecovery,
	} {
		t.Run(prefix, func(t *testing.T) {
			var gotBegin, gotComplete string
			svc := &mockBarrierService{
				claimBeginFn: func(_ context.Context, domain seal.QuorumDomain, username, thumb string) (*service.BarrierShareClaimChallenge, error) {
					if domain != want || username != "alice" {
						t.Fatalf("begin domain=%q username=%q", domain, username)
					}
					gotBegin = thumb
					return &service.BarrierShareClaimChallenge{NonceHex: "abcd"}, nil
				},
				claimCompleteFn: func(_ context.Context, domain seal.QuorumDomain, username, nonce string, sig []byte, thumb string) (*service.BarrierShareClaimResult, error) {
					if domain != want || username != "alice" || nonce != "abcd" || string(sig) != "sig" {
						t.Fatalf("complete domain=%q username=%q nonce=%q sig=%q", domain, username, nonce, sig)
					}
					gotComplete = thumb
					return &service.BarrierShareClaimResult{ShareIndex: 2, Envelope: []byte(`{"v":1}`)}, nil
				},
			}
			srv := newBarrierTestServer(t, svc)

			rec, thumb := holderPost(srv, prefix+"alice/claim/begin", ``, true)
			assertStatus(t, rec, http.StatusOK)
			if gotBegin != thumb {
				t.Fatalf("begin thumbprint = %q, want the TLS peer's %q", gotBegin, thumb)
			}

			rec, thumb = holderPost(srv, prefix+"alice/claim/complete", `{"nonce_hex":"abcd","signature":"c2ln"}`, true)
			assertStatus(t, rec, http.StatusOK)
			if gotComplete != thumb {
				t.Fatalf("complete thumbprint = %q, want the TLS peer's %q", gotComplete, thumb)
			}
			var resp transport.ShareClaimCompleteResponse
			decodeJSON(t, rec, &resp)
			if resp.ShareIndex != 2 || string(resp.Envelope) != `{"v":1}` {
				t.Fatalf("response = %+v", resp)
			}
		})
	}
}

// TestBarrierHolders_Claim_RequiresClientCert proves neither claim step
// reaches the service without a client certificate.
func TestBarrierHolders_Claim_RequiresClientCert(t *testing.T) {
	srv := newBarrierTestServer(t, &mockBarrierService{})
	for _, path := range []string{"/v1/barrier/officers/alice/claim/begin", "/v1/barrier/officers/alice/claim/complete"} {
		rec, _ := holderPost(srv, path, `{}`, false)
		assertStatus(t, rec, http.StatusUnauthorized)
	}
}

func TestBarrierHolders_Claim_Refusals(t *testing.T) {
	svc := &mockBarrierService{
		claimBeginFn: func(context.Context, seal.QuorumDomain, string, string) (*service.BarrierShareClaimChallenge, error) {
			return nil, ceremony.ErrCertThumbprintMismatch
		},
		claimCompleteFn: func(context.Context, seal.QuorumDomain, string, string, []byte, string) (*service.BarrierShareClaimResult, error) {
			return nil, ceremony.ErrShareAlreadyClaimed
		},
	}
	srv := newBarrierTestServer(t, svc)

	rec, _ := holderPost(srv, "/v1/barrier/officers/alice/claim/begin", ``, true)
	assertStatus(t, rec, http.StatusForbidden)
	rec, _ = holderPost(srv, "/v1/barrier/officers/alice/claim/complete", `{"nonce_hex":"ab","signature":"c2ln"}`, true)
	assertStatus(t, rec, http.StatusConflict)
	rec, _ = holderPost(srv, "/v1/barrier/officers/alice/claim/complete", `{bad`, true)
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestBarrierHolders_ClaimPath_Malformed(t *testing.T) {
	srv := newBarrierTestServer(t, &mockBarrierService{})
	for _, path := range []string{
		"/v1/barrier/officers/alice/claim/unknown",
		"/v1/barrier/officers/alice/other/begin",
		"/v1/barrier/officers//claim/begin",
		"/v1/barrier/officers/alice/claim",
	} {
		t.Run(path, func(t *testing.T) {
			rec, _ := holderPost(srv, path, ``, true)
			assertStatus(t, rec, http.StatusNotFound)
		})
	}
}

func TestBarrierHolders_QuorumBegin(t *testing.T) {
	expires := time.Date(2026, 9, 1, 0, 5, 0, 0, time.UTC)
	svc := &mockBarrierService{
		quorumBeginFn: func(_ context.Context, req *service.QuorumBeginRequest) (*service.QuorumChallengeInfo, error) {
			if req.Domain != seal.DomainRecovery || req.Op != seal.OpRecover || req.Username != "bob" || req.Factor != "mtls" {
				t.Fatalf("request = %+v", req)
			}
			return &service.QuorumChallengeInfo{ChallengeID: "c1", Domain: req.Domain, Op: req.Op, Holder: "bob", Factor: "mtls", Nonce: []byte{1, 2}, ExpiresAt: expires}, nil
		},
	}
	srv := newBarrierTestServer(t, svc)
	rec, _ := holderPost(srv, "/v1/barrier/quorum/begin", `{"domain":"recovery","op":"recover","username":"bob","factor":"mtls"}`, false)

	assertStatus(t, rec, http.StatusOK)
	var resp transport.QuorumChallenge
	decodeJSON(t, rec, &resp)
	if resp.ChallengeID != "c1" || resp.Username != "bob" || string(resp.Nonce) != "\x01\x02" || !resp.ExpiresAt.Equal(expires) {
		t.Fatalf("challenge = %+v", resp)
	}
}

func TestBarrierHolders_QuorumBegin_Refusals(t *testing.T) {
	svc := &mockBarrierService{
		quorumBeginFn: func(context.Context, *service.QuorumBeginRequest) (*service.QuorumChallengeInfo, error) {
			return nil, ceremony.ErrTooManyPendingChallenges
		},
	}
	srv := newBarrierTestServer(t, svc)
	rec, _ := holderPost(srv, "/v1/barrier/quorum/begin", `{"domain":"unseal","op":"unseal","username":"bob"}`, false)
	assertStatus(t, rec, http.StatusTooManyRequests)
	rec, _ = holderPost(srv, "/v1/barrier/quorum/begin", `{bad`, false)
	assertStatus(t, rec, http.StatusBadRequest)
}

// TestBarrierHolders_QuorumSubmit_PeerFromConnectionOnly proves the peer
// certificate thumbprint comes from the TLS session and never from the body:
// a body naming one is ignored, and no certificate means an empty one.
func TestBarrierHolders_QuorumSubmit_PeerFromConnectionOnly(t *testing.T) {
	var got *service.QuorumSubmitRequest
	svc := &mockBarrierService{
		quorumSubmitFn: func(_ context.Context, req *service.QuorumSubmitRequest) (*service.QuorumOutcomeInfo, error) {
			cp := *req
			got = &cp
			return &service.QuorumOutcomeInfo{Domain: req.Domain, Op: req.Op, Required: 3, Submitted: 3, Complete: true,
				RootToken: &service.RootTokenInfo{Token: "tok", CreatedAt: time.Unix(1, 0).UTC()}}, nil
		},
	}
	srv := newBarrierTestServer(t, svc)
	body := `{"domain":"unseal","op":"root-token","username":"alice","challenge_id":"c1","factor":"mtls","response":"c2ln","response_metadata":{"a":"b"},"share":"c2hhcmU=","peer_cert_thumbprint":"forged"}`

	rec, thumb := holderPost(srv, "/v1/barrier/quorum/submit", body, true)
	assertStatus(t, rec, http.StatusOK)
	if got.PeerCertThumbprint != thumb {
		t.Fatalf("peer thumbprint = %q, want the TLS peer's %q", got.PeerCertThumbprint, thumb)
	}
	if got.Op != seal.OpRootToken || got.ChallengeID != "c1" || string(got.Response) != "sig" || got.ResponseMetadata["a"] != "b" {
		t.Fatalf("submission = %+v", got)
	}
	var out transport.QuorumOutcome
	decodeJSON(t, rec, &out)
	if !out.Complete || out.RootToken != "tok" || out.Submitted != 3 {
		t.Fatalf("outcome = %+v", out)
	}

	rec, _ = holderPost(srv, "/v1/barrier/quorum/submit", body, false)
	assertStatus(t, rec, http.StatusOK)
	if got.PeerCertThumbprint != "" {
		t.Fatalf("peer thumbprint = %q without a client certificate, want empty", got.PeerCertThumbprint)
	}
}

func TestBarrierHolders_QuorumSubmit_Refusals(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"duplicate holder": {&seal.HolderAlreadySubmittedError{Holder: "alice"}, http.StatusConflict},
		"revoked holder":   {&seal.HolderRevokedError{Holder: "alice"}, http.StatusForbidden},
		"no round":         {seal.ErrQuorumRoundNotOpen, http.StatusConflict},
	} {
		t.Run(name, func(t *testing.T) {
			svc := &mockBarrierService{
				quorumSubmitFn: func(context.Context, *service.QuorumSubmitRequest) (*service.QuorumOutcomeInfo, error) {
					return nil, tc.err
				},
			}
			srv := newBarrierTestServer(t, svc)
			rec, _ := holderPost(srv, "/v1/barrier/quorum/submit", `{"domain":"unseal","op":"unseal","username":"alice","share":"cw=="}`, false)
			assertStatus(t, rec, tc.want)
		})
	}
	srv := newBarrierTestServer(t, &mockBarrierService{})
	rec, _ := holderPost(srv, "/v1/barrier/quorum/submit", `{bad`, false)
	assertStatus(t, rec, http.StatusBadRequest)
}

// TestBarrierHolders_RekeyAndRevoke_PassArguments proves the gated routes
// hand the service the domain, threshold, policy and holder they were given.
func TestBarrierHolders_RekeyAndRevoke_PassArguments(t *testing.T) {
	svc := &mockBarrierService{
		rekeyStartFn: func(_ context.Context, domain seal.QuorumDomain, threshold int, policy seal.FactorPolicy) (*service.QuorumRoundInfo, error) {
			if domain != seal.DomainRecovery || threshold != 3 || policy != seal.FactorPolicyAnyEnrolled {
				t.Fatalf("rekey domain=%q threshold=%d policy=%q", domain, threshold, policy)
			}
			return &service.QuorumRoundInfo{Domain: domain, Op: seal.OpRekey, Required: 2, NewThreshold: 3, Policy: policy}, nil
		},
		officerRevokeFn: func(_ context.Context, domain seal.QuorumDomain, username string, threshold int) (*service.QuorumRoundInfo, error) {
			if domain != seal.DomainUnseal || username != "mallory" || threshold != 2 {
				t.Fatalf("revoke domain=%q username=%q threshold=%d", domain, username, threshold)
			}
			return &service.QuorumRoundInfo{Domain: domain, Op: seal.OpRekey, Required: 3, NewThreshold: 2, Remove: []string{"mallory"}}, nil
		},
	}
	srv := newBarrierPINTestServer(t, svc, newStubPINBackend())

	rec, _ := holderPost(srv, "/v1/barrier/rekey/start", `{"domain":"recovery","threshold":3,"policy":"any-enrolled"}`, false)
	assertStatus(t, rec, http.StatusOK)
	var round transport.QuorumRound
	decodeJSON(t, rec, &round)
	if round.Policy != "any-enrolled" || round.NewThreshold != 3 {
		t.Fatalf("rekey round = %+v", round)
	}

	rec, _ = holderPost(srv, "/v1/barrier/officers/revoke", `{"domain":"unseal","username":"mallory","threshold":2}`, false)
	assertStatus(t, rec, http.StatusOK)
	decodeJSON(t, rec, &round)
	if len(round.Remove) != 1 || round.Remove[0] != "mallory" {
		t.Fatalf("revoke round = %+v", round)
	}
}

func TestBarrierHolders_RekeyAndRevoke_Refusals(t *testing.T) {
	svc := &mockBarrierService{
		rekeyStartFn: func(context.Context, seal.QuorumDomain, int, seal.FactorPolicy) (*service.QuorumRoundInfo, error) {
			return nil, seal.ErrQuorumRoundInProgress
		},
		officerRevokeFn: func(context.Context, seal.QuorumDomain, string, int) (*service.QuorumRoundInfo, error) {
			return nil, &seal.RevocationBreaksQuorumError{Threshold: 3, Remaining: 2}
		},
	}
	srv := newBarrierPINTestServer(t, svc, newStubPINBackend())

	rec, _ := holderPost(srv, "/v1/barrier/rekey/start", `{"domain":"unseal","threshold":2}`, false)
	assertStatus(t, rec, http.StatusConflict)
	rec, _ = holderPost(srv, "/v1/barrier/officers/revoke", `{"domain":"unseal","username":"a","threshold":3}`, false)
	assertStatus(t, rec, http.StatusConflict)
	for _, path := range []string{"/v1/barrier/rekey/start", "/v1/barrier/officers/revoke"} {
		rec, _ = holderPost(srv, path, `{bad`, false)
		assertStatus(t, rec, http.StatusBadRequest)
	}
}

// TestBarrierHolders_NoBarrierService pins the routes' shape without a
// barrier service: the officer routes stay registered and answer 501, and the
// quorum, rekey and revoke routes are not registered at all, like every other
// barrier-only route.
func TestBarrierHolders_NoBarrierService(t *testing.T) {
	srv := newBarrierTestServer(t, nil)
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodPost, "/v1/barrier/officers", http.StatusNotImplemented},
		{http.MethodGet, "/v1/barrier/officers", http.StatusNotImplemented},
		{http.MethodPost, "/v1/barrier/officers/alice/claim/begin", http.StatusNotImplemented},
		{http.MethodPost, "/v1/barrier/officers/alice/claim/complete", http.StatusNotImplemented},
		{http.MethodPost, "/v1/barrier/recovery/officers", http.StatusNotFound},
		{http.MethodPost, "/v1/barrier/quorum/begin", http.StatusNotFound},
		{http.MethodPost, "/v1/barrier/quorum/submit", http.StatusNotFound},
		{http.MethodPost, "/v1/barrier/rekey/start", http.StatusNotFound},
		{http.MethodPost, "/v1/barrier/officers/revoke", http.StatusNotFound},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
			injectFakeTLS(req)
			rec := httptest.NewRecorder()
			srv.dispatch(rec, req)
			assertStatus(t, rec, tc.want)
		})
	}
}

// ---------------------------------------------------------------------------
// Tenant barrier holders
// ---------------------------------------------------------------------------

// TestTenantBarrierHolders_Lifecycle drives every tenant holder route and
// checks each reaches the service with the tenant, arguments and — for the
// claim and submit steps — the connection's certificate.
func TestTenantBarrierHolders_Lifecycle(t *testing.T) {
	var peers []string
	svc := &mockTenantService{
		officerRegisterFn: func(_ context.Context, id string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
			if id != "acme" || req.Username != "carol" || req.Role != seal.RoleCustodian {
				t.Fatalf("register id=%q req=%+v", id, req)
			}
			return &ceremony.RegisterOfficerResult{CertPEM: []byte("cert"), Role: req.Role}, nil
		},
		officerListFn: func(_ context.Context, id string) ([]*ceremony.OfficerStatus, error) {
			return []*ceremony.OfficerStatus{{Username: "carol", Role: seal.RoleCustodian}}, nil
		},
		claimBeginFn: func(_ context.Context, id, username, thumb string) (*ceremony.ChallengeResponse, error) {
			peers = append(peers, thumb)
			return &ceremony.ChallengeResponse{Nonce: []byte{0xab}, Username: username}, nil
		},
		claimCompleteFn: func(_ context.Context, id, username, nonce string, sig []byte, thumb string) (*ceremony.ShareClaimResult, error) {
			peers = append(peers, thumb)
			return &ceremony.ShareClaimResult{ShareIndex: 1, Envelope: []byte("env")}, nil
		},
		quorumBeginFn: func(_ context.Context, id string, op seal.QuorumOp, username, factor string) (*ceremony.QuorumChallenge, error) {
			if op != seal.OpUnseal || username != "carol" || factor != "fido2" {
				t.Fatalf("quorum begin op=%q username=%q factor=%q", op, username, factor)
			}
			return &ceremony.QuorumChallenge{ChallengeID: "c1", Domain: seal.DomainUnseal, Op: op, Holder: username, Factor: factor}, nil
		},
		quorumSubmitFn: func(_ context.Context, id string, sub *seal.HolderSubmission) (*seal.QuorumOutcome, error) {
			peers = append(peers, sub.Proof.PeerCertThumbprint)
			if sub.Domain != seal.DomainUnseal || sub.Holder != "carol" || string(sub.Share) != "share" {
				t.Fatalf("submission = %+v", sub)
			}
			return &seal.QuorumOutcome{Domain: sub.Domain, Op: sub.Op, Required: 2, Submitted: 1}, nil
		},
		rekeyStartFn: func(_ context.Context, id string, threshold int, policy seal.FactorPolicy) (*seal.QuorumRoundInfo, error) {
			if threshold != 2 || policy != seal.FactorPolicyDesignated {
				t.Fatalf("rekey threshold=%d policy=%q", threshold, policy)
			}
			return &seal.QuorumRoundInfo{Domain: seal.DomainUnseal, Op: seal.OpRekey, Required: 2, NewThreshold: 2}, nil
		},
		officerRevokeFn: func(_ context.Context, id, username string, threshold int) (*seal.QuorumRoundInfo, error) {
			return &seal.QuorumRoundInfo{Domain: seal.DomainUnseal, Op: seal.OpRekey, Remove: []string{username}}, nil
		},
	}
	srv := newTenantServer(svc)

	rec, _ := holderPost(srv, "/v1/tenants/acme/barrier/officers", `{"username":"carol","role":"custodian","csr_pem":"csr"}`, false)
	assertStatus(t, rec, http.StatusOK)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/acme/barrier/officers", nil)
	rec = httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var want []string
	rec, thumb := holderPost(srv, "/v1/tenants/acme/barrier/officers/carol/claim/begin", ``, true)
	assertStatus(t, rec, http.StatusOK)
	want = append(want, thumb)
	var begin transport.ShareClaimBeginResponse
	decodeJSON(t, rec, &begin)
	if begin.NonceHex != "ab" {
		t.Fatalf("nonce = %q, want hex of the challenge", begin.NonceHex)
	}

	rec, thumb = holderPost(srv, "/v1/tenants/acme/barrier/officers/carol/claim/complete", `{"nonce_hex":"ab","signature":"c2ln"}`, true)
	assertStatus(t, rec, http.StatusOK)
	want = append(want, thumb)

	rec, _ = holderPost(srv, "/v1/tenants/acme/barrier/quorum/begin", `{"op":"unseal","username":"carol","factor":"fido2"}`, false)
	assertStatus(t, rec, http.StatusOK)

	rec, thumb = holderPost(srv, "/v1/tenants/acme/barrier/quorum/submit", `{"op":"unseal","username":"carol","challenge_id":"c1","factor":"fido2","response":"cg==","share":"c2hhcmU="}`, true)
	assertStatus(t, rec, http.StatusOK)
	want = append(want, thumb)

	rec, _ = holderPost(srv, "/v1/tenants/acme/barrier/rekey/start", `{"threshold":2,"policy":"designated"}`, false)
	assertStatus(t, rec, http.StatusOK)
	rec, _ = holderPost(srv, "/v1/tenants/acme/barrier/officers/revoke", `{"username":"carol","threshold":1}`, false)
	assertStatus(t, rec, http.StatusOK)

	if strings.Join(peers, ",") != strings.Join(want, ",") {
		t.Fatalf("peer thumbprints = %v, want the TLS peers %v", peers, want)
	}
}

func TestTenantBarrierHolders_Refusals(t *testing.T) {
	svc := &mockTenantService{
		officerListFn: func(context.Context, string) ([]*ceremony.OfficerStatus, error) {
			return nil, tenant.ErrTenantNotFound
		},
		quorumSubmitFn: func(context.Context, string, *seal.HolderSubmission) (*seal.QuorumOutcome, error) {
			return nil, &seal.HolderAlreadySubmittedError{Holder: "carol"}
		},
	}
	srv := newTenantServer(svc)

	req := httptest.NewRequest(http.MethodGet, "/v1/tenants/acme/barrier/officers", nil)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	assertStatus(t, rec, http.StatusNotFound)

	rec, _ = holderPost(srv, "/v1/tenants/acme/barrier/quorum/submit", `{"op":"unseal","username":"carol","share":"cw=="}`, false)
	assertStatus(t, rec, http.StatusConflict)

	for _, path := range []string{"/v1/tenants/acme/barrier/officers/carol/claim/begin", "/v1/tenants/acme/barrier/officers/carol/claim/complete"} {
		rec, _ = holderPost(srv, path, `{}`, false)
		assertStatus(t, rec, http.StatusUnauthorized)
	}
	rec, _ = holderPost(srv, "/v1/tenants/acme/barrier/rekey/start", `{bad`, false)
	assertStatus(t, rec, http.StatusBadRequest)
}

// TestTenantBarrierHolders_AuthorizeBeforeDecode proves a caller the gate
// refuses is answered 403 before its request body is parsed: an unauthorized
// caller learns nothing from the decoder about what the route accepts.
func TestTenantBarrierHolders_AuthorizeBeforeDecode(t *testing.T) {
	srv := newTenantServer(&mockTenantService{})
	WithAuthMiddleware(authmw.New(denyingAuthorizer{}))(srv)

	for _, path := range []string{
		"/v1/tenants/acme/barrier/officers",
		"/v1/tenants/acme/barrier/rekey/start",
		"/v1/tenants/acme/barrier/officers/revoke",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{bad`))
			ctx := auth.WithIdentity(req.Context(), &auth.Identity{Subject: "acme-user", TenantID: "acme"})
			rec := httptest.NewRecorder()
			srv.dispatch(rec, req.WithContext(ctx))
			assertStatus(t, rec, http.StatusForbidden)
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("body: %v", err)
			}
		})
	}
}
