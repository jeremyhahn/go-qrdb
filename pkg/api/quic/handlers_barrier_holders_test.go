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

package quic

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

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// quicHolderSvc is a BarrierServicer double recording what the holder
// frames hand it. A nil err answers success.
type quicHolderSvc struct {
	service.BarrierServicer
	err    error
	domain seal.QuorumDomain
	peer   string
	// soPINs records the SO PIN each rekey or revocation handed the service.
	soPINs []string
}

func (h *quicHolderSvc) BarrierOfficerRegister(_ context.Context, d seal.QuorumDomain, r *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	h.domain = d
	if h.err != nil {
		return nil, h.err
	}
	return &service.BarrierOfficerRegisterResult{CertPEM: []byte("cert"), Role: r.Role}, nil
}

func (h *quicHolderSvc) BarrierOfficerList(_ context.Context, d seal.QuorumDomain) ([]*service.BarrierOfficerStatus, error) {
	h.domain = d
	if h.err != nil {
		return nil, h.err
	}
	return []*service.BarrierOfficerStatus{{Username: "carol"}}, nil
}

func (h *quicHolderSvc) BarrierShareClaimBegin(_ context.Context, d seal.QuorumDomain, _ string, peer string) (*service.BarrierShareClaimChallenge, error) {
	h.domain, h.peer = d, peer
	if h.err != nil {
		return nil, h.err
	}
	return &service.BarrierShareClaimChallenge{NonceHex: "ab", ExpiresAt: time.Unix(10, 0)}, nil
}

func (h *quicHolderSvc) BarrierShareClaimComplete(_ context.Context, d seal.QuorumDomain, _, _ string, _ []byte, peer string) (*service.BarrierShareClaimResult, error) {
	h.domain, h.peer = d, peer
	if h.err != nil {
		return nil, h.err
	}
	return &service.BarrierShareClaimResult{ShareIndex: 2, Envelope: []byte("env")}, nil
}

func (h *quicHolderSvc) BarrierQuorumBegin(_ context.Context, r *service.QuorumBeginRequest) (*service.QuorumChallengeInfo, error) {
	if h.err != nil {
		return nil, h.err
	}
	return &service.QuorumChallengeInfo{ChallengeID: "c1", Domain: r.Domain, Op: r.Op}, nil
}

func (h *quicHolderSvc) BarrierQuorumSubmit(_ context.Context, r *service.QuorumSubmitRequest) (*service.QuorumOutcomeInfo, error) {
	h.peer = r.PeerCertThumbprint
	if h.err != nil {
		return nil, h.err
	}
	return &service.QuorumOutcomeInfo{Domain: r.Domain, Op: r.Op, Required: 3, Submitted: 1}, nil
}

func (h *quicHolderSvc) BarrierRekeyStart(_ context.Context, d seal.QuorumDomain, th int, p seal.FactorPolicy, soPIN string) (*service.QuorumRoundInfo, error) {
	h.soPINs = append(h.soPINs, soPIN)
	if h.err != nil {
		return nil, h.err
	}
	return &service.QuorumRoundInfo{Domain: d, NewThreshold: th, Policy: p}, nil
}

func (h *quicHolderSvc) BarrierOfficerRevoke(_ context.Context, d seal.QuorumDomain, u string, _ int, soPIN string) (*service.QuorumRoundInfo, error) {
	h.soPINs = append(h.soPINs, soPIN)
	if h.err != nil {
		return nil, h.err
	}
	return &service.QuorumRoundInfo{Domain: d, Remove: []string{u}}, nil
}

// quicPeerContext binds a TLS client certificate the way the QUIC listener
// does, returning its thumbprint.
func quicPeerContext(t *testing.T) (context.Context, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(4), Subject: pkix.Name{CommonName: "carol"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return auth.WithTLSState(context.Background(), &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}), mtls.Thumbprint(der)
}

func mustMarshal(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// systemHolderFrames are the system-barrier holder frames with valid payloads.
func systemHolderFrames(t *testing.T, s *Server) map[string]func(context.Context) ([]byte, error) {
	reg := mustMarshal(t, &pb.BarrierOfficerRegisterRequest{Username: "carol", Role: "custodian", CsrPem: []byte("csr")})
	begin := mustMarshal(t, &pb.BarrierShareClaimBeginRequest{Username: "carol"})
	complete := mustMarshal(t, &pb.BarrierShareClaimCompleteRequest{Username: "carol", NonceHex: "ab", Signature: []byte("s")})
	return map[string]func(context.Context) ([]byte, error){
		"register":          func(ctx context.Context) ([]byte, error) { return s.handleBarrierOfficerRegister(ctx, reg) },
		"recovery-register": func(ctx context.Context) ([]byte, error) { return s.handleBarrierRecoveryOfficerRegister(ctx, reg) },
		"list":              func(ctx context.Context) ([]byte, error) { return s.handleBarrierOfficerList(ctx, nil) },
		"claim-begin":       func(ctx context.Context) ([]byte, error) { return s.handleBarrierShareClaimBegin(ctx, begin) },
		"claim-complete":    func(ctx context.Context) ([]byte, error) { return s.handleBarrierShareClaimComplete(ctx, complete) },
		"recovery-begin":    func(ctx context.Context) ([]byte, error) { return s.handleBarrierRecoveryShareClaimBegin(ctx, begin) },
		"recovery-complete": func(ctx context.Context) ([]byte, error) {
			return s.handleBarrierRecoveryShareClaimComplete(ctx, complete)
		},
		"quorum-begin": func(ctx context.Context) ([]byte, error) {
			return s.handleBarrierQuorumBegin(ctx, mustMarshal(t, &pb.BarrierQuorumBeginRequest{Domain: "unseal", Op: "unseal", Username: "carol"}))
		},
		"quorum-submit": func(ctx context.Context) ([]byte, error) {
			return s.handleBarrierQuorumSubmit(ctx, mustMarshal(t, &pb.BarrierQuorumSubmitRequest{Domain: "unseal", Op: "unseal", Username: "carol", Share: []byte("s")}))
		},
		"rekey-start": func(ctx context.Context) ([]byte, error) {
			return s.handleBarrierRekeyStart(ctx, mustMarshal(t, &pb.BarrierRekeyStartRequest{Domain: "unseal", Threshold: 2, Policy: "any-enrolled", SoPin: "so-pin-1"}))
		},
		"officer-revoke": func(ctx context.Context) ([]byte, error) {
			return s.handleBarrierOfficerRevoke(ctx, mustMarshal(t, &pb.BarrierOfficerRevokeRequest{Domain: "unseal", Username: "erin", SoPin: "so-pin-1"}))
		},
	}
}

func TestQUICHolders_SystemFrames(t *testing.T) {
	ctx, thumb := quicPeerContext(t)
	svc := &quicHolderSvc{}
	backend := pintest.NewBackend(t)
	if err := backend.SetSOPIN("", "so-pin-1"); err != nil {
		t.Fatal(err)
	}
	s := NewServer(nil, nil, "localhost:0", WithBarrierService(svc), WithPINBackend(backend))
	frames := systemHolderFrames(t, s)
	for name, call := range frames {
		if _, err := call(ctx); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	// Claim steps bind the connection's certificate and route the domain.
	if _, err := frames["recovery-complete"](ctx); err != nil || svc.domain != seal.DomainRecovery || svc.peer != thumb {
		t.Fatalf("recovery complete: %v domain=%s peer=%q", err, svc.domain, svc.peer)
	}
	// A submission without a certificate carries no peer.
	if _, err := frames["quorum-submit"](context.Background()); err != nil || svc.peer != "" {
		t.Fatalf("submit without a certificate: %v peer=%q", err, svc.peer)
	}
	for _, name := range []string{"claim-begin", "claim-complete", "recovery-begin", "recovery-complete"} {
		_, err := frames[name](context.Background())
		if err == nil {
			t.Fatalf("%s without a certificate succeeded", name)
		}
		if code, _ := mapServiceError(err); code != wire.StatusUnauthenticated {
			t.Errorf("%s without a certificate = %v (status %d), want StatusUnauthenticated", name, err, code)
		}
	}
}

func TestQUICHolders_SystemRefusals(t *testing.T) {
	ctx, _ := quicPeerContext(t)

	failing := NewServer(nil, nil, "localhost:0", WithBarrierService(&quicHolderSvc{err: ceremony.ErrShareAlreadyClaimed}), WithPINBackend(pintest.NewBackend(t)))
	for name, call := range systemHolderFrames(t, failing) {
		if _, err := call(ctx); !errors.Is(err, ceremony.ErrShareAlreadyClaimed) {
			t.Errorf("%s = %v, want the service error", name, err)
		}
	}

	unconfigured := NewServer(nil, nil, "localhost:0")
	for name, call := range systemHolderFrames(t, unconfigured) {
		var notConfigured *ServiceNotConfiguredError
		if _, err := call(ctx); !errors.As(err, &notConfigured) {
			t.Errorf("%s = %v, want ServiceNotConfiguredError", name, err)
		}
	}

	// The SO PIN gating rekey and revocation is the barrier service's to
	// verify; the handler hands it the request's so_pin.
	recording := &quicHolderSvc{}
	frames := systemHolderFrames(t, NewServer(nil, nil, "localhost:0", WithBarrierService(recording)))
	for _, name := range []string{"rekey-start", "officer-revoke"} {
		if _, err := frames[name](ctx); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(recording.soPINs) != 2 {
		t.Errorf("SO PINs handed to the service = %q", recording.soPINs)
	}

	garbage := []byte{0xff, 0xfe}
	s := NewServer(nil, nil, "localhost:0", WithBarrierService(&quicHolderSvc{}))
	for name, call := range map[string]func() ([]byte, error){
		"register":       func() ([]byte, error) { return s.handleBarrierOfficerRegister(ctx, garbage) },
		"claim-begin":    func() ([]byte, error) { return s.handleBarrierShareClaimBegin(ctx, garbage) },
		"claim-complete": func() ([]byte, error) { return s.handleBarrierShareClaimComplete(ctx, garbage) },
		"quorum-begin":   func() ([]byte, error) { return s.handleBarrierQuorumBegin(ctx, garbage) },
		"quorum-submit":  func() ([]byte, error) { return s.handleBarrierQuorumSubmit(ctx, garbage) },
		"rekey-start":    func() ([]byte, error) { return s.handleBarrierRekeyStart(ctx, garbage) },
		"officer-revoke": func() ([]byte, error) { return s.handleBarrierOfficerRevoke(ctx, garbage) },
		"bad-factor-params": func() ([]byte, error) {
			return s.handleBarrierOfficerRegister(ctx, mustMarshal(t, &pb.BarrierOfficerRegisterRequest{Factors: []*pb.HolderFactorEnrolment{{Name: "x", ParamsJson: []byte("[")}}}))
		},
	} {
		if _, err := call(); err == nil {
			t.Errorf("%s accepted a malformed payload", name)
		}
	}
}

// quicHolderTenant is a TenantServicer double for the tenant holder frames.
type quicHolderTenant struct {
	tenant.TenantServicer
	err error
}

func (h *quicHolderTenant) TenantBarrierOfficerRegister(_ context.Context, _ string, r *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	if h.err != nil {
		return nil, h.err
	}
	return &ceremony.RegisterOfficerResult{CertPEM: []byte("cert"), Role: r.Role}, nil
}

func (h *quicHolderTenant) TenantBarrierOfficerList(context.Context, string) ([]*ceremony.OfficerStatus, error) {
	if h.err != nil {
		return nil, h.err
	}
	return []*ceremony.OfficerStatus{{Username: "carol"}}, nil
}

func (h *quicHolderTenant) TenantBarrierShareClaimBegin(context.Context, string, string, string) (*ceremony.ChallengeResponse, error) {
	if h.err != nil {
		return nil, h.err
	}
	return &ceremony.ChallengeResponse{Nonce: []byte{0xab}}, nil
}

func (h *quicHolderTenant) TenantBarrierShareClaimComplete(context.Context, string, string, string, []byte, string) (*ceremony.ShareClaimResult, error) {
	if h.err != nil {
		return nil, h.err
	}
	return &ceremony.ShareClaimResult{ShareIndex: 1, Envelope: []byte("env")}, nil
}

func (h *quicHolderTenant) TenantBarrierQuorumBegin(_ context.Context, _ string, op seal.QuorumOp, u, f string) (*ceremony.QuorumChallenge, error) {
	if h.err != nil {
		return nil, h.err
	}
	return &ceremony.QuorumChallenge{ChallengeID: "c1", Op: op, Holder: u, Factor: f}, nil
}

func (h *quicHolderTenant) TenantBarrierQuorumSubmit(_ context.Context, _ string, sub *seal.HolderSubmission) (*seal.QuorumOutcome, error) {
	if h.err != nil {
		return nil, h.err
	}
	return &seal.QuorumOutcome{Domain: sub.Domain, Op: sub.Op, Required: 2, Submitted: 1}, nil
}

func (h *quicHolderTenant) TenantBarrierRekeyStart(context.Context, string, int, seal.FactorPolicy) (*seal.QuorumRoundInfo, error) {
	if h.err != nil {
		return nil, h.err
	}
	return &seal.QuorumRoundInfo{Op: seal.OpRekey}, nil
}

func (h *quicHolderTenant) TenantBarrierOfficerRevoke(_ context.Context, _, u string, _ int) (*seal.QuorumRoundInfo, error) {
	if h.err != nil {
		return nil, h.err
	}
	return &seal.QuorumRoundInfo{Op: seal.OpRekey, Remove: []string{u}}, nil
}

// tenantHolderFrames are the tenant holder frames for tenant id.
func tenantHolderFrames(t *testing.T, s *Server, id string) map[string]func(context.Context) ([]byte, error) {
	frame := func(h func(context.Context, []byte) ([]byte, error), m proto.Message) func(context.Context) ([]byte, error) {
		payload := mustMarshal(t, m)
		return func(ctx context.Context) ([]byte, error) { return h(ctx, payload) }
	}
	return map[string]func(context.Context) ([]byte, error){
		"register":       frame(s.handleTenantBarrierOfficerRegister, &pb.TenantBarrierOfficerRegisterRequest{TenantId: id, Username: "carol", Role: "custodian"}),
		"list":           frame(s.handleTenantBarrierOfficerList, &pb.TenantBarrierOfficerListRequest{TenantId: id}),
		"claim-begin":    frame(s.handleTenantBarrierShareClaimBegin, &pb.TenantBarrierShareClaimBeginRequest{TenantId: id, Username: "carol"}),
		"claim-complete": frame(s.handleTenantBarrierShareClaimComplete, &pb.TenantBarrierShareClaimCompleteRequest{TenantId: id, Username: "carol"}),
		"quorum-begin":   frame(s.handleTenantBarrierQuorumBegin, &pb.TenantBarrierQuorumBeginRequest{TenantId: id, Op: "unseal", Username: "carol"}),
		"quorum-submit":  frame(s.handleTenantBarrierQuorumSubmit, &pb.TenantBarrierQuorumSubmitRequest{TenantId: id, Op: "unseal", Username: "carol", Share: []byte("s")}),
		"rekey-start":    frame(s.handleTenantBarrierRekeyStart, &pb.TenantBarrierRekeyStartRequest{TenantId: id, Threshold: 2}),
		"officer-revoke": frame(s.handleTenantBarrierOfficerRevoke, &pb.TenantBarrierOfficerRevokeRequest{TenantId: id, Username: "erin"}),
	}
}

func TestQUICHolders_TenantFrames(t *testing.T) {
	ctx, _ := quicPeerContext(t)
	s := NewServer(nil, nil, "localhost:0", WithTenantService(&quicHolderTenant{}))
	for name, call := range tenantHolderFrames(t, s, "acme") {
		if _, err := call(ctx); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, call := range tenantHolderFrames(t, s, "") {
		var invalid *InvalidArgumentError
		if _, err := call(ctx); !errors.As(err, &invalid) {
			t.Errorf("%s without a tenant = %v", name, err)
		}
	}
	for _, name := range []string{"claim-begin", "claim-complete"} {
		_, err := tenantHolderFrames(t, s, "acme")[name](context.Background())
		if err == nil {
			t.Fatalf("%s ran without a certificate", name)
		}
		if code, _ := mapServiceError(err); code != wire.StatusUnauthenticated {
			t.Errorf("%s without a certificate = %v (status %d), want StatusUnauthenticated", name, err, code)
		}
	}

	failing := NewServer(nil, nil, "localhost:0", WithTenantService(&quicHolderTenant{err: tenant.ErrTenantNotFound}))
	for name, call := range tenantHolderFrames(t, failing, "acme") {
		if _, err := call(ctx); !errors.Is(err, tenant.ErrTenantNotFound) {
			t.Errorf("%s = %v, want the service error", name, err)
		}
	}
	unconfigured := NewServer(nil, nil, "localhost:0")
	for name, call := range tenantHolderFrames(t, unconfigured, "acme") {
		var notConfigured *ServiceNotConfiguredError
		if _, err := call(ctx); !errors.As(err, &notConfigured) {
			t.Errorf("%s = %v, want ServiceNotConfiguredError", name, err)
		}
	}
	denied := NewServer(nil, nil, "localhost:0", WithTenantService(&quicHolderTenant{}), WithAuthMiddleware(authmw.New(quicDenyAuthorizer{}, authmw.WithRoleResolver(authtest.BuiltInRoles(t)))))
	deniedCtx := auth.WithIdentity(ctx, &auth.Identity{Subject: "mallory", TenantID: "acme"})
	for _, name := range []string{"register", "list", "rekey-start", "officer-revoke"} {
		if _, err := tenantHolderFrames(t, denied, "acme")[name](deniedCtx); err == nil {
			t.Errorf("%s was not refused", name)
		}
	}
	for name, h := range map[string]func(context.Context, []byte) ([]byte, error){
		"register": s.handleTenantBarrierOfficerRegister, "list": s.handleTenantBarrierOfficerList,
		"claim-begin": s.handleTenantBarrierShareClaimBegin, "claim-complete": s.handleTenantBarrierShareClaimComplete,
		"quorum-begin": s.handleTenantBarrierQuorumBegin, "quorum-submit": s.handleTenantBarrierQuorumSubmit,
		"rekey-start": s.handleTenantBarrierRekeyStart, "officer-revoke": s.handleTenantBarrierOfficerRevoke,
	} {
		if _, err := h(ctx, []byte{0xff, 0xfe}); err == nil {
			t.Errorf("%s accepted a malformed payload", name)
		}
	}
	bad := mustMarshal(t, &pb.TenantBarrierOfficerRegisterRequest{TenantId: "acme", Factors: []*pb.HolderFactorEnrolment{{Name: "x", ParamsJson: []byte("[")}}})
	if _, err := s.handleTenantBarrierOfficerRegister(ctx, bad); err == nil {
		t.Error("malformed factor parameters were accepted")
	}
}

// quicDenyAuthorizer refuses every RBAC decision.
type quicDenyAuthorizer struct{}

func (quicDenyAuthorizer) Authorize(context.Context, *auth.Identity, string, string) error {
	return auth.ErrUnauthorized
}
