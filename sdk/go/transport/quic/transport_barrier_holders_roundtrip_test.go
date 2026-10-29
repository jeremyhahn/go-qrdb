package quic

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"sync"
	"testing"
	"time"

	quicserver "github.com/jeremyhahn/go-qrdb/pkg/api/quic"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// holderBarrierSvc records what the barrier holder handlers hand the service
// and answers with fixed results; every other BarrierServicer method is the
// nil embedded interface.
type holderBarrierSvc struct {
	service.BarrierServicer

	err error

	mu  sync.Mutex
	rec holderBarrierCalls
}

// holderBarrierCalls is what holderBarrierSvc last saw. The QUIC server calls
// the double on a handler goroutine, so a test reads it through seen.
type holderBarrierCalls struct {
	domain     seal.QuorumDomain
	username   string
	thumbprint string
	nonceHex   string
	signature  []byte
	register   *service.BarrierOfficerRegisterRequest
	begin      *service.QuorumBeginRequest
	submit     *service.QuorumSubmitRequest
	threshold  int
	soPIN      string
	unsealed   bool
}

// seen returns a copy of what the double has recorded so far.
func (m *holderBarrierSvc) seen() holderBarrierCalls {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rec
}

var _ service.BarrierServicer = (*holderBarrierSvc)(nil)

func (m *holderBarrierSvc) BarrierClusterUnseal(context.Context) (*clusterseal.Report, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rec.unsealed = true
	if m.err != nil {
		return nil, m.err
	}
	return &clusterseal.Report{Active: true, Epoch: 7, Principal: "root", Node: "n1", At: time.Unix(1700000000, 0).UTC(), Local: clusterseal.LocalServing}, nil
}

func (m *holderBarrierSvc) BarrierOfficerRegister(_ context.Context, domain seal.QuorumDomain, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rec.domain, m.rec.register = domain, req
	if m.err != nil {
		return nil, m.err
	}
	return &service.BarrierOfficerRegisterResult{CertPEM: []byte("cert-" + string(domain)), Role: req.Role, Designated: "mtls", Factors: []string{"mtls"}}, nil
}

func (m *holderBarrierSvc) BarrierOfficerList(_ context.Context, domain seal.QuorumDomain) ([]*service.BarrierOfficerStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rec.domain = domain
	if m.err != nil {
		return nil, m.err
	}
	return []*service.BarrierOfficerStatus{{Username: "alice", Role: seal.RoleSecurityOfficer, ShareIndex: 1, ClaimedAt: time.Unix(1700000100, 0)}}, nil
}

func (m *holderBarrierSvc) BarrierShareClaimBegin(_ context.Context, domain seal.QuorumDomain, username, thumbprint string) (*service.BarrierShareClaimChallenge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rec.domain, m.rec.username, m.rec.thumbprint = domain, username, thumbprint
	if m.err != nil {
		return nil, m.err
	}
	return &service.BarrierShareClaimChallenge{NonceHex: "beef", ExpiresAt: time.Unix(1700000600, 0)}, nil
}

func (m *holderBarrierSvc) BarrierShareClaimComplete(_ context.Context, domain seal.QuorumDomain, username, nonceHex string, signature []byte, thumbprint string) (*service.BarrierShareClaimResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rec.domain, m.rec.username, m.rec.nonceHex, m.rec.signature, m.rec.thumbprint = domain, username, nonceHex, signature, thumbprint
	if m.err != nil {
		return nil, m.err
	}
	return &service.BarrierShareClaimResult{ShareIndex: 4, Envelope: []byte("envelope-" + string(domain))}, nil
}

func (m *holderBarrierSvc) BarrierQuorumBegin(_ context.Context, req *service.QuorumBeginRequest) (*service.QuorumChallengeInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rec.begin = req
	if m.err != nil {
		return nil, m.err
	}
	return &ceremony.QuorumChallenge{ChallengeID: "q-1", Domain: req.Domain, Op: req.Op, Holder: req.Username, Factor: "mtls", Nonce: []byte("n"), ExpiresAt: time.Unix(1700000900, 0)}, nil
}

func (m *holderBarrierSvc) BarrierQuorumSubmit(_ context.Context, req *service.QuorumSubmitRequest) (*service.QuorumOutcomeInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rec.submit = &service.QuorumSubmitRequest{Domain: req.Domain, Op: req.Op, Username: req.Username, ChallengeID: req.ChallengeID,
		Factor: req.Factor, Response: append([]byte(nil), req.Response...), PeerCertThumbprint: req.PeerCertThumbprint}
	if m.err != nil {
		return nil, m.err
	}
	return &service.QuorumOutcomeInfo{Domain: req.Domain, Op: req.Op, Required: 2, Submitted: 1}, nil
}

func (m *holderBarrierSvc) BarrierOfficerRevoke(_ context.Context, domain seal.QuorumDomain, username string, threshold int, soPIN string) (*service.QuorumRoundInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rec.domain, m.rec.username, m.rec.threshold, m.rec.soPIN = domain, username, threshold, soPIN
	if m.err != nil {
		return nil, m.err
	}
	return &seal.QuorumRoundInfo{Domain: domain, Op: seal.OpRekey, Required: 2, Submitted: 0, ExpiresAt: time.Unix(1700001000, 0), NewThreshold: threshold, Remove: []string{username}}, nil
}

// startMTLSRoundTripServer is startRoundTripServer with the client presenting
// the harness certificate, so handlers that bind a holder to the caller's
// certificate thumbprint see one.
func startMTLSRoundTripServer(t *testing.T, opts ...quicserver.ServerOption) *Transport {
	t.Helper()
	serverCfg, clientCfg := e2eTLS(t)
	pool := x509.NewCertPool()
	pool.AddCert(serverCfg.Certificates[0].Leaf)
	serverCfg.ClientAuth = tls.RequireAndVerifyClientCert
	serverCfg.ClientCAs = pool
	clientCfg.Certificates = serverCfg.Certificates
	return startRoundTripServerTLS(t, serverCfg, clientCfg, opts...)
}

func TestQUICRoundTrip_BarrierHolders_RegisterAndList(t *testing.T) {
	svc := &holderBarrierSvc{}
	tr := startRoundTripServer(t, quicserver.WithBarrierService(svc))
	ctx := context.Background()

	req := &transport.OfficerRegisterRequest{Username: "alice", Role: string(seal.RoleSecurityOfficer), CSRPEM: "csr-pem", Designated: "mtls"}
	reg, err := tr.OfficerRegister(ctx, req)
	if err != nil {
		t.Fatalf("OfficerRegister: %v", err)
	}
	if svc.seen().domain != seal.DomainUnseal || svc.seen().register.Username != "alice" || string(svc.seen().register.CSRPEM) != "csr-pem" || svc.seen().register.Role != seal.RoleSecurityOfficer {
		t.Fatalf("service saw domain %q request %+v", svc.seen().domain, svc.seen().register)
	}
	if reg.CertPEM != "cert-unseal" || reg.Role != string(seal.RoleSecurityOfficer) || len(reg.Factors) != 1 || reg.Factors[0] != "mtls" {
		t.Fatalf("OfficerRegister = %+v", reg)
	}
	reg, err = tr.RecoveryOfficerRegister(ctx, req)
	if err != nil || svc.seen().domain != seal.DomainRecovery || reg.CertPEM != "cert-recovery" {
		t.Fatalf("RecoveryOfficerRegister: err %v, domain %q, response %+v", err, svc.seen().domain, reg)
	}

	officers, err := tr.OfficerList(ctx)
	if err != nil {
		t.Fatalf("OfficerList: %v", err)
	}
	if svc.seen().domain != seal.DomainUnseal || len(officers) != 1 || officers[0].Username != "alice" || officers[0].ShareIndex != 1 || !officers[0].ClaimedAt.Equal(time.Unix(1700000100, 0)) {
		t.Fatalf("OfficerList: domain %q officers %+v", svc.seen().domain, officers)
	}
}

func TestQUICRoundTrip_BarrierHolders_ShareClaimBothDomains(t *testing.T) {
	svc := &holderBarrierSvc{}
	tr := startMTLSRoundTripServer(t, quicserver.WithBarrierService(svc))
	ctx := context.Background()

	for _, tc := range []struct {
		domain   seal.QuorumDomain
		begin    func(context.Context, *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error)
		complete func(context.Context, *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error)
	}{
		{seal.DomainUnseal, tr.ShareClaimBegin, tr.ShareClaimComplete},
		{seal.DomainRecovery, tr.RecoveryShareClaimBegin, tr.RecoveryShareClaimComplete},
	} {
		svc.mu.Lock()
		svc.rec.thumbprint = ""
		svc.mu.Unlock()
		ch, err := tc.begin(ctx, &transport.ShareClaimBeginRequest{Username: "alice"})
		if err != nil {
			t.Fatalf("%s claim begin: %v", tc.domain, err)
		}
		if svc.seen().domain != tc.domain || svc.seen().username != "alice" || svc.seen().thumbprint == "" || ch.NonceHex != "beef" || !ch.ExpiresAt.Equal(time.Unix(1700000600, 0)) {
			t.Fatalf("%s claim begin: service saw %q/%q/%q, returned %+v", tc.domain, svc.seen().domain, svc.seen().username, svc.seen().thumbprint, ch)
		}
		got, err := tc.complete(ctx, &transport.ShareClaimCompleteRequest{Username: "alice", NonceHex: "beef", Signature: []byte("sig")})
		if err != nil {
			t.Fatalf("%s claim complete: %v", tc.domain, err)
		}
		if svc.seen().nonceHex != "beef" || string(svc.seen().signature) != "sig" || got.ShareIndex != 4 || string(got.Envelope) != "envelope-"+string(tc.domain) {
			t.Fatalf("%s claim complete: service saw %q/%q, returned %+v", tc.domain, svc.seen().nonceHex, svc.seen().signature, got)
		}
	}
}

func TestQUICRoundTrip_BarrierHolders_QuorumRevokeAndClusterUnseal(t *testing.T) {
	svc := &holderBarrierSvc{}
	tr := startMTLSRoundTripServer(t, quicserver.WithBarrierService(svc))
	ctx := context.Background()

	ch, err := tr.BarrierQuorumBegin(ctx, &transport.QuorumBeginRequest{Domain: "unseal", Op: "unseal", Username: "alice", Factor: "mtls"})
	if err != nil {
		t.Fatalf("BarrierQuorumBegin: %v", err)
	}
	if svc.seen().begin == nil || svc.seen().begin.Username != "alice" || svc.seen().begin.Domain != seal.DomainUnseal || ch.ChallengeID != "q-1" || ch.Username != "alice" || string(ch.Nonce) != "n" {
		t.Fatalf("BarrierQuorumBegin: service saw %+v, returned %+v", svc.seen().begin, ch)
	}

	out, err := tr.BarrierQuorumSubmit(ctx, &transport.QuorumSubmitRequest{Domain: "unseal", Op: "unseal", Username: "alice",
		ChallengeID: "q-1", Factor: "mtls", Response: []byte("resp"), Share: []byte("share")})
	if err != nil {
		t.Fatalf("BarrierQuorumSubmit: %v", err)
	}
	if s := svc.seen().submit; s == nil || s.ChallengeID != "q-1" || string(s.Response) != "resp" || s.PeerCertThumbprint == "" {
		t.Fatalf("BarrierQuorumSubmit: service saw %+v", s)
	}
	if out.Required != 2 || out.Submitted != 1 || out.Complete || out.Domain != "unseal" {
		t.Fatalf("BarrierQuorumSubmit = %+v", out)
	}

	round, err := tr.BarrierOfficerRevoke(ctx, &transport.OfficerRevokeRequest{Domain: "unseal", Username: "bob", Threshold: 2, SOPin: "so-pin"})
	if err != nil {
		t.Fatalf("BarrierOfficerRevoke: %v", err)
	}
	if svc.seen().domain != seal.DomainUnseal || svc.seen().username != "bob" || svc.seen().threshold != 2 || svc.seen().soPIN != "so-pin" {
		t.Fatalf("BarrierOfficerRevoke: service saw %q/%q/%d/%q", svc.seen().domain, svc.seen().username, svc.seen().threshold, svc.seen().soPIN)
	}
	if round.Required != 2 || round.NewThreshold != 2 || len(round.Remove) != 1 || round.Remove[0] != "bob" || !round.ExpiresAt.Equal(time.Unix(1700001000, 0)) {
		t.Fatalf("BarrierOfficerRevoke = %+v", round)
	}
	pinCtx, release, err := transport.WithSOPIN(ctx, []byte("bound-pin"))
	if err != nil {
		t.Fatalf("WithSOPIN: %v", err)
	}
	defer release()
	if _, err := tr.BarrierOfficerRevoke(pinCtx, &transport.OfficerRevokeRequest{Domain: "unseal", Username: "carol"}); err != nil || svc.seen().soPIN != "bound-pin" {
		t.Fatalf("BarrierOfficerRevoke with a bound PIN: err %v, service saw %q", err, svc.seen().soPIN)
	}
	var conflict *transport.SOPINConflictError
	if _, err := tr.BarrierOfficerRevoke(pinCtx, &transport.OfficerRevokeRequest{Domain: "unseal", Username: "dave", SOPin: "other"}); !errors.As(err, &conflict) {
		t.Fatalf("BarrierOfficerRevoke with a conflicting PIN: err = %v, want *transport.SOPINConflictError", err)
	}
	if svc.seen().username == "dave" {
		t.Fatal("a conflicting SO PIN reached the service")
	}

	rep, err := tr.BarrierClusterUnseal(ctx)
	if err != nil {
		t.Fatalf("BarrierClusterUnseal: %v", err)
	}
	if !svc.seen().unsealed || !rep.Active || rep.Epoch != 7 || rep.Principal != "root" {
		t.Fatalf("BarrierClusterUnseal = %+v (service reached: %v)", rep, svc.seen().unsealed)
	}
}

func TestQUICRoundTrip_BarrierHolders_ServiceErrors(t *testing.T) {
	svc := &holderBarrierSvc{err: errors.New("holder refused")}
	tr := startMTLSRoundTripServer(t, quicserver.WithBarrierService(svc))
	ctx := context.Background()
	calls := map[string]func() error{
		"OfficerRegister": func() error {
			_, err := tr.OfficerRegister(ctx, &transport.OfficerRegisterRequest{Username: "alice", Role: string(seal.RoleSecurityOfficer)})
			return err
		},
		"OfficerList": func() error { _, err := tr.OfficerList(ctx); return err },
		"ShareClaimBegin": func() error {
			_, err := tr.ShareClaimBegin(ctx, &transport.ShareClaimBeginRequest{Username: "alice"})
			return err
		},
		"ShareClaimComplete": func() error {
			_, err := tr.ShareClaimComplete(ctx, &transport.ShareClaimCompleteRequest{Username: "alice"})
			return err
		},
		"BarrierQuorumBegin": func() error {
			_, err := tr.BarrierQuorumBegin(ctx, &transport.QuorumBeginRequest{Domain: "unseal", Op: "unseal", Username: "alice"})
			return err
		},
		"BarrierQuorumSubmit": func() error {
			_, err := tr.BarrierQuorumSubmit(ctx, &transport.QuorumSubmitRequest{Domain: "unseal", Op: "unseal", Username: "alice"})
			return err
		},
		"BarrierOfficerRevoke": func() error {
			_, err := tr.BarrierOfficerRevoke(ctx, &transport.OfficerRevokeRequest{Domain: "unseal", Username: "bob", SOPin: "pin"})
			return err
		},
		"BarrierClusterUnseal": func() error { _, err := tr.BarrierClusterUnseal(ctx); return err },
	}
	for name, call := range calls {
		if err := call(); err == nil {
			t.Errorf("%s: a service refusal returned no error", name)
		}
	}
}

// TestQUICRoundTrip_BarrierHolders_NilRequest proves every barrier holder
// method refuses a nil request with a typed error instead of dereferencing it.
func TestQUICRoundTrip_BarrierHolders_NilRequest(t *testing.T) {
	svc := &holderBarrierSvc{}
	tr := startRoundTripServer(t, quicserver.WithBarrierService(svc))
	ctx := context.Background()
	calls := map[string]func() error{
		"OfficerRegister":            func() error { _, err := tr.OfficerRegister(ctx, nil); return err },
		"RecoveryOfficerRegister":    func() error { _, err := tr.RecoveryOfficerRegister(ctx, nil); return err },
		"ShareClaimBegin":            func() error { _, err := tr.ShareClaimBegin(ctx, nil); return err },
		"ShareClaimComplete":         func() error { _, err := tr.ShareClaimComplete(ctx, nil); return err },
		"RecoveryShareClaimBegin":    func() error { _, err := tr.RecoveryShareClaimBegin(ctx, nil); return err },
		"RecoveryShareClaimComplete": func() error { _, err := tr.RecoveryShareClaimComplete(ctx, nil); return err },
		"BarrierQuorumBegin":         func() error { _, err := tr.BarrierQuorumBegin(ctx, nil); return err },
		"BarrierQuorumSubmit":        func() error { _, err := tr.BarrierQuorumSubmit(ctx, nil); return err },
		"BarrierOfficerRevoke":       func() error { _, err := tr.BarrierOfficerRevoke(ctx, nil); return err },
		"BarrierRekeyStart":          func() error { _, err := tr.BarrierRekeyStart(ctx, nil); return err },
	}
	for name, call := range calls {
		err := call()
		requireQUICRequestError(t, err, name)
		var nilReq *transport.NilRequestError
		if !errors.As(err, &nilReq) {
			t.Errorf("%s: err = %v, want *transport.NilRequestError", name, err)
		}
	}
	if svc.seen().register != nil || svc.seen().begin != nil || svc.seen().submit != nil || svc.seen().username != "" {
		t.Fatal("a nil request reached the barrier service")
	}
}
