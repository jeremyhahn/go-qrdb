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

package grpc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	grpcpeer "google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	tenantpkg "github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// holderBarrierSvc is a BarrierServicer double for the share-holder RPCs.
// Every holder method delegates to its fn; a method the test did not set
// answers errHolderUnset so an unexpected call fails loudly.
type holderBarrierSvc struct {
	service.BarrierServicer
	register      func(seal.QuorumDomain, *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error)
	list          func(seal.QuorumDomain) ([]*service.BarrierOfficerStatus, error)
	claimBegin    func(seal.QuorumDomain, string, string) (*service.BarrierShareClaimChallenge, error)
	claimComplete func(seal.QuorumDomain, string, string, []byte, string) (*service.BarrierShareClaimResult, error)
	quorumBegin   func(*service.QuorumBeginRequest) (*service.QuorumChallengeInfo, error)
	quorumSubmit  func(*service.QuorumSubmitRequest) (*service.QuorumOutcomeInfo, error)
	rekeyStart    func(seal.QuorumDomain, int, seal.FactorPolicy) (*service.QuorumRoundInfo, error)
	revoke        func(seal.QuorumDomain, string, int) (*service.QuorumRoundInfo, error)
	// soPINs records the SO PIN each rekey or revocation handed the service,
	// which verifies it.
	soPINs []string
}

var errHolderUnset = errors.New("holderBarrierSvc: not configured by this test")

func (h *holderBarrierSvc) BarrierOfficerRegister(_ context.Context, d seal.QuorumDomain, r *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	if h.register == nil {
		return nil, errHolderUnset
	}
	return h.register(d, r)
}

func (h *holderBarrierSvc) BarrierOfficerList(_ context.Context, d seal.QuorumDomain) ([]*service.BarrierOfficerStatus, error) {
	if h.list == nil {
		return nil, errHolderUnset
	}
	return h.list(d)
}

func (h *holderBarrierSvc) BarrierShareClaimBegin(_ context.Context, d seal.QuorumDomain, u, th string) (*service.BarrierShareClaimChallenge, error) {
	if h.claimBegin == nil {
		return nil, errHolderUnset
	}
	return h.claimBegin(d, u, th)
}

func (h *holderBarrierSvc) BarrierShareClaimComplete(_ context.Context, d seal.QuorumDomain, u, n string, sig []byte, th string) (*service.BarrierShareClaimResult, error) {
	if h.claimComplete == nil {
		return nil, errHolderUnset
	}
	return h.claimComplete(d, u, n, sig, th)
}

func (h *holderBarrierSvc) BarrierQuorumBegin(_ context.Context, r *service.QuorumBeginRequest) (*service.QuorumChallengeInfo, error) {
	if h.quorumBegin == nil {
		return nil, errHolderUnset
	}
	return h.quorumBegin(r)
}

func (h *holderBarrierSvc) BarrierQuorumSubmit(_ context.Context, r *service.QuorumSubmitRequest) (*service.QuorumOutcomeInfo, error) {
	if h.quorumSubmit == nil {
		return nil, errHolderUnset
	}
	return h.quorumSubmit(r)
}

func (h *holderBarrierSvc) BarrierRekeyStart(_ context.Context, d seal.QuorumDomain, th int, p seal.FactorPolicy, soPIN string) (*service.QuorumRoundInfo, error) {
	h.soPINs = append(h.soPINs, soPIN)
	if h.rekeyStart == nil {
		return nil, errHolderUnset
	}
	return h.rekeyStart(d, th, p)
}

func (h *holderBarrierSvc) BarrierOfficerRevoke(_ context.Context, d seal.QuorumDomain, u string, th int, soPIN string) (*service.QuorumRoundInfo, error) {
	h.soPINs = append(h.soPINs, soPIN)
	if h.revoke == nil {
		return nil, errHolderUnset
	}
	return h.revoke(d, u, th)
}

// holderPeer returns a context carrying a verified TLS peer certificate and
// the thumbprint the handlers must derive from it.
func holderPeer(t *testing.T) (context.Context, string) {
	t.Helper()
	cert := holderCert(t)
	return verifiedPeerContext(cert), mtls.Thumbprint(cert.Raw)
}

// holderCert returns a self-signed client certificate for carol.
func holderCert(t *testing.T) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "carol"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// verifiedPeerContext is a context whose connection presents cert and whose
// handshake verified it, bound the way the auth interceptor binds the TLS
// state.
func verifiedPeerContext(cert *x509.Certificate) context.Context {
	return peerContext(&tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{cert},
		VerifiedChains:   [][]*x509.Certificate{{cert}},
	})
}

// unverifiedPeerContext is a context whose connection presents cert but
// whose handshake built no verified chain, as on a listener whose trust pool
// verifies afterwards through the verifier the auth interceptor binds.
func unverifiedPeerContext(cert *x509.Certificate) context.Context {
	return peerContext(&tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}})
}

// peerContext binds state as both the gRPC peer's TLS info and the
// request's TLS state, as the auth interceptor does.
func peerContext(state *tls.ConnectionState) context.Context {
	ctx := grpcpeer.NewContext(context.Background(), &grpcpeer.Peer{
		AuthInfo: credentials.TLSInfo{State: *state},
	})
	return auth.WithTLSState(ctx, state)
}

func requireCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if got := status.Code(err); got != want {
		t.Fatalf("code = %s (%v), want %s", got, err, want)
	}
}

func TestGRPCHolders_RegisterAndList(t *testing.T) {
	var gotDomain seal.QuorumDomain
	svc := &holderBarrierSvc{
		register: func(d seal.QuorumDomain, r *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
			gotDomain = d
			if r.Username == "self" {
				return nil, ceremony.ErrSelfEnrolment
			}
			if r.Role != seal.RoleCustodian || r.Designated != "fido2" || len(r.Factors) != 1 {
				t.Fatalf("request = %+v", r)
			}
			return &service.BarrierOfficerRegisterResult{CertPEM: []byte("cert"), Role: r.Role, Designated: "fido2"}, nil
		},
		list: func(d seal.QuorumDomain) ([]*service.BarrierOfficerStatus, error) {
			return []*service.BarrierOfficerStatus{{Username: "carol", Role: seal.RoleCustodian}}, nil
		},
	}
	s := &qrdbServiceServer{barrierSvc: svc}
	req := &pb.BarrierOfficerRegisterRequest{Username: "carol", Role: "custodian", CsrPem: []byte("csr"), Designated: "fido2",
		Factors: []*pb.HolderFactorEnrolment{{Name: "fido2", ParamsJson: []byte(`{"public_key":"k"}`)}}}

	resp, err := s.BarrierOfficerRegister(context.Background(), req)
	if err != nil || gotDomain != seal.DomainUnseal || string(resp.GetCertPem()) != "cert" || resp.GetRole() != "custodian" {
		t.Fatalf("register = %+v, %v (domain %s)", resp, err, gotDomain)
	}
	if _, err := s.BarrierRecoveryOfficerRegister(context.Background(), req); err != nil || gotDomain != seal.DomainRecovery {
		t.Fatalf("recovery register: %v (domain %s)", err, gotDomain)
	}
	_, err = s.BarrierOfficerRegister(context.Background(), &pb.BarrierOfficerRegisterRequest{Factors: []*pb.HolderFactorEnrolment{{Name: "x", ParamsJson: []byte("[")}}})
	requireCode(t, err, codes.InvalidArgument)
	_, err = s.BarrierOfficerRegister(context.Background(), &pb.BarrierOfficerRegisterRequest{Username: "self"})
	requireCode(t, err, codes.PermissionDenied)

	list, err := s.BarrierOfficerList(context.Background(), &emptypb.Empty{})
	if err != nil || len(list.GetOfficers()) != 1 || list.GetOfficers()[0].GetRole() != "custodian" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	svc.list = func(seal.QuorumDomain) ([]*service.BarrierOfficerStatus, error) {
		return nil, service.ErrCeremonyNotConfigured
	}
	_, err = s.BarrierOfficerList(context.Background(), &emptypb.Empty{})
	requireCode(t, err, codes.FailedPrecondition)
}

func TestGRPCHolders_ClaimBindsTheTLSPeer(t *testing.T) {
	ctx, thumb := holderPeer(t)
	var domains []seal.QuorumDomain
	svc := &holderBarrierSvc{
		claimBegin: func(d seal.QuorumDomain, u, th string) (*service.BarrierShareClaimChallenge, error) {
			domains = append(domains, d)
			if th != thumb || u != "carol" {
				t.Fatalf("begin user=%q thumb=%q", u, th)
			}
			return &service.BarrierShareClaimChallenge{NonceHex: "ab", ExpiresAt: time.Unix(10, 0)}, nil
		},
		claimComplete: func(d seal.QuorumDomain, u, n string, sig []byte, th string) (*service.BarrierShareClaimResult, error) {
			domains = append(domains, d)
			if th != thumb || n != "ab" || string(sig) != "sig" {
				t.Fatalf("complete nonce=%q sig=%q thumb=%q", n, sig, th)
			}
			return &service.BarrierShareClaimResult{ShareIndex: 2, Envelope: []byte("env")}, nil
		},
	}
	s := &qrdbServiceServer{barrierSvc: svc}
	begin := &pb.BarrierShareClaimBeginRequest{Username: "carol"}
	complete := &pb.BarrierShareClaimCompleteRequest{Username: "carol", NonceHex: "ab", Signature: []byte("sig")}

	if r, err := s.BarrierShareClaimBegin(ctx, begin); err != nil || r.GetNonceHex() != "ab" {
		t.Fatalf("begin = %+v, %v", r, err)
	}
	if r, err := s.BarrierShareClaimComplete(ctx, complete); err != nil || string(r.GetEnvelope()) != "env" || r.GetShareIndex() != 2 {
		t.Fatalf("complete = %+v, %v", r, err)
	}
	if _, err := s.BarrierRecoveryShareClaimBegin(ctx, begin); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BarrierRecoveryShareClaimComplete(ctx, complete); err != nil {
		t.Fatal(err)
	}
	if len(domains) != 4 || domains[2] != seal.DomainRecovery || domains[3] != seal.DomainRecovery {
		t.Fatalf("domains = %v", domains)
	}

	// No client certificate: neither step reaches the service, and the
	// caller is unauthenticated, as on every other transport.
	_, err := s.BarrierShareClaimBegin(context.Background(), begin)
	requireCode(t, err, codes.Unauthenticated)
	_, err = s.BarrierShareClaimComplete(context.Background(), complete)
	requireCode(t, err, codes.Unauthenticated)
	_, err = s.BarrierRecoveryShareClaimBegin(context.Background(), begin)
	requireCode(t, err, codes.Unauthenticated)
	_, err = s.BarrierRecoveryShareClaimComplete(context.Background(), complete)
	requireCode(t, err, codes.Unauthenticated)

	svc.claimBegin = func(seal.QuorumDomain, string, string) (*service.BarrierShareClaimChallenge, error) {
		return nil, ceremony.ErrCertThumbprintMismatch
	}
	_, err = s.BarrierShareClaimBegin(ctx, begin)
	requireCode(t, err, codes.PermissionDenied)
	svc.claimComplete = func(seal.QuorumDomain, string, string, []byte, string) (*service.BarrierShareClaimResult, error) {
		return nil, ceremony.ErrShareAlreadyClaimed
	}
	_, err = s.BarrierShareClaimComplete(ctx, complete)
	requireCode(t, err, codes.FailedPrecondition)
}

func TestGRPCHolders_QuorumTakesThePeerFromTheConnection(t *testing.T) {
	ctx, thumb := holderPeer(t)
	var got *service.QuorumSubmitRequest
	svc := &holderBarrierSvc{
		quorumBegin: func(r *service.QuorumBeginRequest) (*service.QuorumChallengeInfo, error) {
			if r.Domain != seal.DomainRecovery || r.Op != seal.OpRecover || r.Username != "carol" || r.Factor != "fido2" {
				t.Fatalf("begin = %+v", r)
			}
			return &service.QuorumChallengeInfo{ChallengeID: "c1", Holder: "carol", Factor: "fido2", Nonce: []byte{1}}, nil
		},
		quorumSubmit: func(r *service.QuorumSubmitRequest) (*service.QuorumOutcomeInfo, error) {
			cp := *r
			got = &cp
			return &service.QuorumOutcomeInfo{Domain: r.Domain, Op: r.Op, Required: 3, Submitted: 1}, nil
		},
	}
	s := &qrdbServiceServer{barrierSvc: svc}
	ch, err := s.BarrierQuorumBegin(ctx, &pb.BarrierQuorumBeginRequest{Domain: "recovery", Op: "recover", Username: "carol", Factor: "fido2"})
	if err != nil || ch.GetChallengeId() != "c1" {
		t.Fatalf("begin = %+v, %v", ch, err)
	}
	out, err := s.BarrierQuorumSubmit(ctx, &pb.BarrierQuorumSubmitRequest{Domain: "recovery", Op: "recover", Username: "carol", ChallengeId: "c1", Share: []byte("s")})
	if err != nil || out.GetSubmitted() != 1 || got.PeerCertThumbprint != thumb {
		t.Fatalf("submit = %+v, %v (peer %q)", out, err, got.PeerCertThumbprint)
	}
	if _, err := s.BarrierQuorumSubmit(context.Background(), &pb.BarrierQuorumSubmitRequest{Share: []byte("s")}); err != nil || got.PeerCertThumbprint != "" {
		t.Fatalf("submit without a certificate: %v (peer %q)", err, got.PeerCertThumbprint)
	}

	svc.quorumBegin = func(*service.QuorumBeginRequest) (*service.QuorumChallengeInfo, error) {
		return nil, ceremony.ErrTooManyPendingChallenges
	}
	_, err = s.BarrierQuorumBegin(ctx, &pb.BarrierQuorumBeginRequest{})
	requireCode(t, err, codes.ResourceExhausted)
	svc.quorumSubmit = func(*service.QuorumSubmitRequest) (*service.QuorumOutcomeInfo, error) {
		return nil, &seal.HolderAlreadySubmittedError{Holder: "carol"}
	}
	_, err = s.BarrierQuorumSubmit(ctx, &pb.BarrierQuorumSubmitRequest{Share: []byte("s")})
	requireCode(t, err, codes.AlreadyExists)
}

// TestGRPCHolders_RekeyAndRevokeCarryTheSOPIN: rekey start and revocation are
// SO-PIN-gated, and the barrier service verifies the PIN behind its lockout,
// so every transport gets the same check. The handler hands the request's
// so_pin to the service, and renders the service's PIN refusals by class.
func TestGRPCHolders_RekeyAndRevokeCarryTheSOPIN(t *testing.T) {
	round := &service.QuorumRoundInfo{Domain: seal.DomainUnseal, Op: seal.OpRekey, Required: 3, NewThreshold: 2, Remove: []string{"erin"}}
	svc := &holderBarrierSvc{
		rekeyStart: func(d seal.QuorumDomain, th int, p seal.FactorPolicy) (*service.QuorumRoundInfo, error) {
			if d != seal.DomainUnseal || th != 2 || p != seal.FactorPolicyAnyEnrolled {
				t.Fatalf("rekey %s %d %s", d, th, p)
			}
			return round, nil
		},
		revoke: func(d seal.QuorumDomain, u string, th int) (*service.QuorumRoundInfo, error) {
			if u != "erin" || th != 2 {
				t.Fatalf("revoke %s %d", u, th)
			}
			return round, nil
		},
	}
	rekey := &pb.BarrierRekeyStartRequest{Domain: "unseal", Threshold: 2, Policy: "any-enrolled", SoPin: "so-pin-1"}
	revoke := &pb.BarrierOfficerRevokeRequest{Domain: "unseal", Username: "erin", Threshold: 2, SoPin: "so-pin-2"}

	s := &qrdbServiceServer{barrierSvc: svc}
	if r, err := s.BarrierRekeyStart(context.Background(), rekey); err != nil || r.GetNewThreshold() != 2 {
		t.Fatalf("rekey = %+v, %v", r, err)
	}
	if r, err := s.BarrierOfficerRevoke(context.Background(), revoke); err != nil || len(r.GetRemove()) != 1 {
		t.Fatalf("revoke = %+v, %v", r, err)
	}
	if len(svc.soPINs) != 2 || svc.soPINs[0] != "so-pin-1" || svc.soPINs[1] != "so-pin-2" {
		t.Fatalf("SO PINs handed to the service = %q", svc.soPINs)
	}

	for refusal, code := range map[error]codes.Code{
		pin.ErrBackendNotConfigured:          codes.FailedPrecondition,
		pin.ErrPINNotSet:                     codes.FailedPrecondition,
		pin.ErrPINInvalid:                    codes.Unauthenticated,
		&pin.SOPINLockedError{RetryAfter: 1}: codes.ResourceExhausted,
		seal.ErrQuorumRoundInProgress:        codes.AlreadyExists,
	} {
		svc.rekeyStart = func(seal.QuorumDomain, int, seal.FactorPolicy) (*service.QuorumRoundInfo, error) { return nil, refusal }
		_, err := s.BarrierRekeyStart(context.Background(), rekey)
		requireCode(t, err, code)
	}
	svc.revoke = func(seal.QuorumDomain, string, int) (*service.QuorumRoundInfo, error) {
		return nil, &seal.RevocationBreaksQuorumError{Threshold: 3, Remaining: 2}
	}
	_, err := s.BarrierOfficerRevoke(context.Background(), revoke)
	requireCode(t, err, codes.FailedPrecondition)
}

func TestGRPCHolders_NoBarrierService(t *testing.T) {
	s := &qrdbServiceServer{}
	ctx := context.Background()
	for name, call := range map[string]func() error{
		"register":    func() error { _, err := s.BarrierOfficerRegister(ctx, &pb.BarrierOfficerRegisterRequest{}); return err },
		"list":        func() error { _, err := s.BarrierOfficerList(ctx, &emptypb.Empty{}); return err },
		"claim-begin": func() error { _, err := s.BarrierShareClaimBegin(ctx, &pb.BarrierShareClaimBeginRequest{}); return err },
		"claim-complete": func() error {
			_, err := s.BarrierShareClaimComplete(ctx, &pb.BarrierShareClaimCompleteRequest{})
			return err
		},
		"quorum-begin":   func() error { _, err := s.BarrierQuorumBegin(ctx, &pb.BarrierQuorumBeginRequest{}); return err },
		"quorum-submit":  func() error { _, err := s.BarrierQuorumSubmit(ctx, &pb.BarrierQuorumSubmitRequest{}); return err },
		"rekey-start":    func() error { _, err := s.BarrierRekeyStart(ctx, &pb.BarrierRekeyStartRequest{}); return err },
		"officer-revoke": func() error { _, err := s.BarrierOfficerRevoke(ctx, &pb.BarrierOfficerRevokeRequest{}); return err },
		"recovery-register": func() error {
			_, err := s.BarrierRecoveryOfficerRegister(ctx, &pb.BarrierOfficerRegisterRequest{})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) { requireCode(t, call(), codes.Unimplemented) })
	}
}

func TestMapHolderError_NonHolderErrorIsInternal(t *testing.T) {
	requireCode(t, mapServiceError(errors.New("disk on fire")), codes.Internal)
	requireCode(t, mapServiceError(ceremony.ErrOfficerNotFound), codes.NotFound)
}

// holderTenantSvc is a TenantServicer double for the tenant holder RPCs.
type holderTenantSvc struct {
	tenantpkg.TenantServicer
	err error
	// peers records the thumbprint each share claim reaching the service
	// was bound to.
	peers []string
}

func (h *holderTenantSvc) TenantBarrierOfficerRegister(_ context.Context, _ string, r *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	if h.err != nil {
		return nil, h.err
	}
	return &ceremony.RegisterOfficerResult{CertPEM: []byte("cert"), Role: r.Role}, nil
}

func (h *holderTenantSvc) TenantBarrierOfficerList(context.Context, string) ([]*ceremony.OfficerStatus, error) {
	if h.err != nil {
		return nil, h.err
	}
	return []*ceremony.OfficerStatus{{Username: "carol"}}, nil
}

func (h *holderTenantSvc) TenantBarrierShareClaimBegin(_ context.Context, _, _, peer string) (*ceremony.ChallengeResponse, error) {
	h.peers = append(h.peers, peer)
	if h.err != nil {
		return nil, h.err
	}
	return &ceremony.ChallengeResponse{Nonce: []byte{0xab}, ExpiresAt: time.Unix(10, 0)}, nil
}

func (h *holderTenantSvc) TenantBarrierShareClaimComplete(_ context.Context, _, _, _ string, _ []byte, peer string) (*ceremony.ShareClaimResult, error) {
	h.peers = append(h.peers, peer)
	if h.err != nil {
		return nil, h.err
	}
	return &ceremony.ShareClaimResult{ShareIndex: 1, Envelope: []byte("env")}, nil
}

func (h *holderTenantSvc) TenantBarrierQuorumBegin(_ context.Context, _ string, op seal.QuorumOp, u, f string) (*ceremony.QuorumChallenge, error) {
	if h.err != nil {
		return nil, h.err
	}
	return &ceremony.QuorumChallenge{ChallengeID: "c1", Op: op, Holder: u, Factor: f}, nil
}

func (h *holderTenantSvc) TenantBarrierQuorumSubmit(_ context.Context, _ string, sub *seal.HolderSubmission) (*seal.QuorumOutcome, error) {
	if h.err != nil {
		return nil, h.err
	}
	return &seal.QuorumOutcome{Domain: sub.Domain, Op: sub.Op, Required: 2, Submitted: 1}, nil
}

func (h *holderTenantSvc) TenantBarrierRekeyStart(context.Context, string, int, seal.FactorPolicy) (*seal.QuorumRoundInfo, error) {
	if h.err != nil {
		return nil, h.err
	}
	return &seal.QuorumRoundInfo{Op: seal.OpRekey, Required: 2}, nil
}

func (h *holderTenantSvc) TenantBarrierOfficerRevoke(_ context.Context, _, u string, _ int) (*seal.QuorumRoundInfo, error) {
	if h.err != nil {
		return nil, h.err
	}
	return &seal.QuorumRoundInfo{Op: seal.OpRekey, Remove: []string{u}}, nil
}

// tenantHolderCalls drives every tenant holder RPC for tenant id.
func tenantHolderCalls(s *tenantBarrierServer, ctx context.Context, id string) map[string]func() error {
	return map[string]func() error{
		"register": func() error {
			_, err := s.OfficerRegister(ctx, &pb.TenantBarrierOfficerRegisterRequest{TenantId: id, Username: "carol", Role: "custodian"})
			return err
		},
		"list": func() error {
			_, err := s.OfficerList(ctx, &pb.TenantBarrierOfficerListRequest{TenantId: id})
			return err
		},
		"claim-begin": func() error {
			_, err := s.ShareClaimBegin(ctx, &pb.TenantBarrierShareClaimBeginRequest{TenantId: id, Username: "carol"})
			return err
		},
		"claim-complete": func() error {
			_, err := s.ShareClaimComplete(ctx, &pb.TenantBarrierShareClaimCompleteRequest{TenantId: id, Username: "carol"})
			return err
		},
		"quorum-begin": func() error {
			_, err := s.QuorumBegin(ctx, &pb.TenantBarrierQuorumBeginRequest{TenantId: id, Op: "unseal", Username: "carol"})
			return err
		},
		"quorum-submit": func() error {
			_, err := s.QuorumSubmit(ctx, &pb.TenantBarrierQuorumSubmitRequest{TenantId: id, Op: "unseal", Username: "carol", Share: []byte("s")})
			return err
		},
		"rekey-start": func() error {
			_, err := s.RekeyStart(ctx, &pb.TenantBarrierRekeyStartRequest{TenantId: id, Threshold: 2})
			return err
		},
		"officer-revoke": func() error {
			_, err := s.OfficerRevoke(ctx, &pb.TenantBarrierOfficerRevokeRequest{TenantId: id, Username: "erin"})
			return err
		},
	}
}

func TestGRPCTenantHolders_Lifecycle(t *testing.T) {
	ctx, _ := holderPeer(t)
	s := &tenantBarrierServer{svc: &holderTenantSvc{}}
	for name, call := range tenantHolderCalls(s, ctx, "acme") {
		if err := call(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestGRPCTenantHolders_Refusals(t *testing.T) {
	ctx, _ := holderPeer(t)

	unconfigured := &tenantBarrierServer{}
	for name, call := range tenantHolderCalls(unconfigured, ctx, "acme") {
		t.Run("unconfigured/"+name, func(t *testing.T) { requireCode(t, call(), codes.Unimplemented) })
	}
	configured := &tenantBarrierServer{svc: &holderTenantSvc{}}
	for name, call := range tenantHolderCalls(configured, ctx, "") {
		t.Run("no-tenant/"+name, func(t *testing.T) { requireCode(t, call(), codes.InvalidArgument) })
	}
	failing := &tenantBarrierServer{svc: &holderTenantSvc{err: tenantpkg.ErrTenantNotFound}}
	for name, call := range tenantHolderCalls(failing, ctx, "acme") {
		t.Run("not-found/"+name, func(t *testing.T) { requireCode(t, call(), codes.NotFound) })
	}
	denied := &tenantBarrierServer{svc: &holderTenantSvc{}, authMW: authmw.New(denyAllAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))}
	deniedCtx := auth.WithIdentity(ctx, &auth.Identity{Subject: "mallory", TenantID: "acme"})
	for _, name := range []string{"register", "list", "rekey-start", "officer-revoke"} {
		t.Run("denied/"+name, func(t *testing.T) {
			requireCode(t, tenantHolderCalls(denied, deniedCtx, "acme")[name](), codes.PermissionDenied)
		})
	}

	// A claim needs the holder's certificate on the connection; without one
	// the caller is unauthenticated, as on every other transport.
	for _, name := range []string{"claim-begin", "claim-complete"} {
		t.Run("no-cert/"+name, func(t *testing.T) {
			requireCode(t, tenantHolderCalls(configured, context.Background(), "acme")[name](), codes.Unauthenticated)
		})
	}
}

// claimVerifier is the listener's trust pool as auth.IssuerVerifier: it
// answers chain, or refuses with err, and counts its calls.
type claimVerifier struct {
	chain []*x509.Certificate
	err   error
	calls int
}

var _ auth.IssuerVerifier = (*claimVerifier)(nil)

func (v *claimVerifier) VerifyPeerChain(context.Context, *x509.Certificate, []*x509.Certificate) ([]*x509.Certificate, error) {
	v.calls++
	return v.chain, v.err
}

func (v *claimVerifier) IsClusterCA(context.Context, *x509.Certificate) bool { return false }

func (v *claimVerifier) VerifyIssuer(context.Context, string, []*x509.Certificate) error { return nil }

// trustStateError is a verifier refusal caused by a failed trust-state
// read, not by the chain.
type trustStateError struct{}

func (trustStateError) Error() string               { return "trust state read failed" }
func (trustStateError) TrustStateUnavailable() bool { return true }

// grpcClaimCalls returns the share-claim RPCs of the system, recovery and
// tenant domains, over services that record the thumbprint each claim
// reaching them was bound to, and a reader of those thumbprints.
func grpcClaimCalls() (map[string]func(context.Context) error, func() []string) {
	var bound []string
	barrier := &holderBarrierSvc{
		claimBegin: func(_ seal.QuorumDomain, _, th string) (*service.BarrierShareClaimChallenge, error) {
			bound = append(bound, th)
			return &service.BarrierShareClaimChallenge{NonceHex: "ab"}, nil
		},
		claimComplete: func(_ seal.QuorumDomain, _, _ string, _ []byte, th string) (*service.BarrierShareClaimResult, error) {
			bound = append(bound, th)
			return &service.BarrierShareClaimResult{}, nil
		},
	}
	tenant := &holderTenantSvc{}
	s := &qrdbServiceServer{barrierSvc: barrier}
	ts := &tenantBarrierServer{svc: tenant}
	begin := &pb.BarrierShareClaimBeginRequest{Username: "carol"}
	complete := &pb.BarrierShareClaimCompleteRequest{Username: "carol", NonceHex: "ab", Signature: []byte("sig")}
	calls := map[string]func(context.Context) error{
		"system-begin": func(ctx context.Context) error {
			_, err := s.BarrierShareClaimBegin(ctx, begin)
			return err
		},
		"system-complete": func(ctx context.Context) error {
			_, err := s.BarrierShareClaimComplete(ctx, complete)
			return err
		},
		"recovery-begin": func(ctx context.Context) error {
			_, err := s.BarrierRecoveryShareClaimBegin(ctx, begin)
			return err
		},
		"recovery-complete": func(ctx context.Context) error {
			_, err := s.BarrierRecoveryShareClaimComplete(ctx, complete)
			return err
		},
		"tenant-begin": func(ctx context.Context) error {
			_, err := ts.ShareClaimBegin(ctx, &pb.TenantBarrierShareClaimBeginRequest{TenantId: "acme", Username: "carol"})
			return err
		},
		"tenant-complete": func(ctx context.Context) error {
			_, err := ts.ShareClaimComplete(ctx, &pb.TenantBarrierShareClaimCompleteRequest{TenantId: "acme", Username: "carol"})
			return err
		},
	}
	return calls, func() []string { return append(append([]string(nil), bound...), tenant.peers...) }
}

// A share claim binds the holder's certificate, so a certificate the
// connection presented but no one verified binds nothing: every claim RPC
// refuses it as Unauthenticated before the service is called, whether the
// listener bound no verifier or its verifier refused the chain, and a
// failed trust-state read is a retryable Unavailable.
func TestGRPCHolders_ClaimRefusesAnUnverifiedCertificate(t *testing.T) {
	cert := holderCert(t)
	refusing := &claimVerifier{err: errors.New("x509: certificate signed by unknown authority")}
	cases := []struct {
		name string
		ctx  context.Context
		want codes.Code
	}{
		{"no-verifier", unverifiedPeerContext(cert), codes.Unauthenticated},
		{"verifier-refuses", auth.WithPeerChainVerifier(unverifiedPeerContext(cert), refusing), codes.Unauthenticated},
		{"trust-state-unavailable", auth.WithPeerChainVerifier(unverifiedPeerContext(cert), &claimVerifier{err: trustStateError{}}), codes.Unavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls, bound := grpcClaimCalls()
			for name, call := range calls {
				if got := status.Code(call(tc.ctx)); got != tc.want {
					t.Errorf("%s: code = %s, want %s", name, got, tc.want)
				}
			}
			if got := bound(); len(got) != 0 {
				t.Fatalf("claims reached the service bound to %v", got)
			}
		})
	}
	if refusing.calls != 6 {
		t.Fatalf("refusing verifier consulted %d times, want 6", refusing.calls)
	}
}

// On a listener whose trust pool verifies the client certificate after the
// handshake, crypto/tls builds no VerifiedChains; every claim RPC binds the
// leaf of the chain the bound verifier verified.
func TestGRPCHolders_ClaimBindsThePoolVerifiedLeaf(t *testing.T) {
	cert := holderCert(t)
	pool := &claimVerifier{chain: []*x509.Certificate{cert}}
	ctx := auth.WithPeerChainVerifier(unverifiedPeerContext(cert), pool)
	calls, bound := grpcClaimCalls()
	for name, call := range calls {
		if err := call(ctx); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	want := mtls.Thumbprint(cert.Raw)
	got := bound()
	if len(got) != len(calls) {
		t.Fatalf("bound %d claims, want %d", len(got), len(calls))
	}
	for _, th := range got {
		if th != want {
			t.Fatalf("claim bound %q, want %q", th, want)
		}
	}
	if pool.calls != len(calls) {
		t.Fatalf("pool consulted %d times, want %d", pool.calls, len(calls))
	}
}

// denyAllAuthorizer refuses every RBAC decision.
type denyAllAuthorizer struct{}

func (denyAllAuthorizer) Authorize(context.Context, *auth.Identity, string, string) error {
	return auth.ErrUnauthorized
}
