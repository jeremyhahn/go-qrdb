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

package unix

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// holderBarrierSvc is a service.BarrierServicer that answers the cluster
// barrier's holder RPCs the way BarrierService does: it records the domain
// and arguments the server handler passed and returns either its canned
// result or err. Anything else panics through the nil embedded interface.
type holderBarrierSvc struct {
	service.BarrierServicer

	err error

	domain     seal.QuorumDomain
	username   string
	thumbprint string
	nonceHex   string
	signature  []byte
	register   *service.BarrierOfficerRegisterRequest
	begin      *service.QuorumBeginRequest
	unsealed   bool
}

var _ service.BarrierServicer = (*holderBarrierSvc)(nil)

func (m *holderBarrierSvc) BarrierClusterUnseal(context.Context) (*clusterseal.Report, error) {
	m.unsealed = true
	if m.err != nil {
		return nil, m.err
	}
	return &clusterseal.Report{Active: true, Epoch: 7, Principal: "root", Node: "n1", At: time.Unix(1700000000, 0).UTC(), Local: clusterseal.LocalServing}, nil
}

func (m *holderBarrierSvc) BarrierOfficerRegister(_ context.Context, domain seal.QuorumDomain, req *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	m.domain, m.register = domain, req
	if m.err != nil {
		return nil, m.err
	}
	return &service.BarrierOfficerRegisterResult{CertPEM: []byte("cert-" + string(domain)), Role: req.Role, Designated: "mtls", Factors: []string{"mtls"}}, nil
}

func (m *holderBarrierSvc) BarrierOfficerList(_ context.Context, domain seal.QuorumDomain) ([]*service.BarrierOfficerStatus, error) {
	m.domain = domain
	if m.err != nil {
		return nil, m.err
	}
	return []*service.BarrierOfficerStatus{{Username: "alice", Role: seal.RoleSecurityOfficer, ShareIndex: 1, ClaimedAt: time.Unix(1700000100, 0)}}, nil
}

func (m *holderBarrierSvc) BarrierShareClaimBegin(_ context.Context, domain seal.QuorumDomain, username, thumbprint string) (*service.BarrierShareClaimChallenge, error) {
	m.domain, m.username, m.thumbprint = domain, username, thumbprint
	if m.err != nil {
		return nil, m.err
	}
	return &service.BarrierShareClaimChallenge{NonceHex: "beef", ExpiresAt: time.Unix(1700000600, 0)}, nil
}

func (m *holderBarrierSvc) BarrierShareClaimComplete(_ context.Context, domain seal.QuorumDomain, username, nonceHex string, signature []byte, thumbprint string) (*service.BarrierShareClaimResult, error) {
	m.domain, m.username, m.nonceHex, m.signature, m.thumbprint = domain, username, nonceHex, signature, thumbprint
	if m.err != nil {
		return nil, m.err
	}
	return &service.BarrierShareClaimResult{ShareIndex: 4, Envelope: []byte("envelope-" + string(domain))}, nil
}

func (m *holderBarrierSvc) BarrierQuorumBegin(_ context.Context, req *service.QuorumBeginRequest) (*service.QuorumChallengeInfo, error) {
	m.begin = req
	if m.err != nil {
		return nil, m.err
	}
	return &ceremony.QuorumChallenge{ChallengeID: "q-1", Domain: req.Domain, Op: req.Op, Holder: req.Username, Factor: "mtls", Nonce: []byte("n"), ExpiresAt: time.Unix(1700000900, 0)}, nil
}

func TestUnixRT_BarrierHolders_RegisterBothDomainsAndList(t *testing.T) {
	svc := &holderBarrierSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithBarrierService(svc))
	defer cleanup()
	ctx := context.Background()

	req := &transport.OfficerRegisterRequest{Username: "alice", Role: string(seal.RoleSecurityOfficer), CSRPEM: "csr-pem", Designated: "mtls"}
	reg, err := tr.OfficerRegister(ctx, req)
	if err != nil {
		t.Fatalf("OfficerRegister: %v", err)
	}
	if svc.domain != seal.DomainUnseal || svc.register.Username != "alice" || string(svc.register.CSRPEM) != "csr-pem" || svc.register.Role != seal.RoleSecurityOfficer {
		t.Fatalf("service saw domain %q request %+v", svc.domain, svc.register)
	}
	if reg.CertPEM != "cert-unseal" || reg.Role != string(seal.RoleSecurityOfficer) || !reflect.DeepEqual(reg.Factors, []string{"mtls"}) {
		t.Fatalf("OfficerRegister = %+v", reg)
	}

	reg, err = tr.RecoveryOfficerRegister(ctx, req)
	if err != nil {
		t.Fatalf("RecoveryOfficerRegister: %v", err)
	}
	if svc.domain != seal.DomainRecovery || reg.CertPEM != "cert-recovery" {
		t.Fatalf("RecoveryOfficerRegister: domain %q response %+v", svc.domain, reg)
	}

	officers, err := tr.OfficerList(ctx)
	if err != nil {
		t.Fatalf("OfficerList: %v", err)
	}
	if svc.domain != seal.DomainUnseal || len(officers) != 1 || officers[0].Username != "alice" || officers[0].ShareIndex != 1 || !officers[0].ClaimedAt.Equal(time.Unix(1700000100, 0)) {
		t.Fatalf("OfficerList: domain %q officers %+v", svc.domain, officers)
	}
}

func TestUnixRT_BarrierHolders_ShareClaimBothDomains(t *testing.T) {
	svc := &holderBarrierSvc{}
	tr, cleanup := startMTLSServer(t, unixserver.WithBarrierService(svc))
	defer cleanup()
	ctx := context.Background()

	type claim struct {
		domain   seal.QuorumDomain
		begin    func(context.Context, *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error)
		complete func(context.Context, *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error)
	}
	for _, c := range []claim{
		{seal.DomainUnseal, tr.ShareClaimBegin, tr.ShareClaimComplete},
		{seal.DomainRecovery, tr.RecoveryShareClaimBegin, tr.RecoveryShareClaimComplete},
	} {
		begin, err := c.begin(ctx, &transport.ShareClaimBeginRequest{Username: "alice"})
		if err != nil {
			t.Fatalf("%s claim begin: %v", c.domain, err)
		}
		if svc.domain != c.domain || svc.username != "alice" || svc.thumbprint == "" {
			t.Fatalf("%s claim begin: service saw domain %q user %q thumbprint %q", c.domain, svc.domain, svc.username, svc.thumbprint)
		}
		if begin.NonceHex != "beef" || !begin.ExpiresAt.Equal(time.Unix(1700000600, 0)) {
			t.Fatalf("%s claim begin = %+v", c.domain, begin)
		}

		done, err := c.complete(ctx, &transport.ShareClaimCompleteRequest{Username: "alice", NonceHex: begin.NonceHex, Signature: []byte("sig")})
		if err != nil {
			t.Fatalf("%s claim complete: %v", c.domain, err)
		}
		if svc.domain != c.domain || svc.nonceHex != "beef" || !bytes.Equal(svc.signature, []byte("sig")) {
			t.Fatalf("%s claim complete: service saw domain %q nonce %q signature %q", c.domain, svc.domain, svc.nonceHex, svc.signature)
		}
		if done.ShareIndex != 4 || string(done.Envelope) != "envelope-"+string(c.domain) {
			t.Fatalf("%s claim complete = %+v", c.domain, done)
		}
	}
}

func TestUnixRT_BarrierHolders_QuorumBeginAndClusterUnseal(t *testing.T) {
	svc := &holderBarrierSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithBarrierService(svc))
	defer cleanup()
	ctx := context.Background()

	ch, err := tr.BarrierQuorumBegin(ctx, &transport.QuorumBeginRequest{Domain: string(seal.DomainRecovery), Op: string(seal.OpRecover), Username: "alice", Factor: "fido2"})
	if err != nil {
		t.Fatalf("BarrierQuorumBegin: %v", err)
	}
	if b := svc.begin; b.Domain != seal.DomainRecovery || b.Op != seal.OpRecover || b.Username != "alice" || b.Factor != "fido2" {
		t.Fatalf("service saw %+v", b)
	}
	if ch.ChallengeID != "q-1" || ch.Domain != string(seal.DomainRecovery) || ch.Op != string(seal.OpRecover) || ch.Username != "alice" || !ch.ExpiresAt.Equal(time.Unix(1700000900, 0)) {
		t.Fatalf("QuorumChallenge = %+v", ch)
	}

	cs, err := tr.BarrierClusterUnseal(ctx)
	if err != nil {
		t.Fatalf("BarrierClusterUnseal: %v", err)
	}
	if !svc.unsealed || !cs.Active || cs.Sealed || cs.Epoch != 7 || cs.Principal != "root" || cs.Node != "n1" || cs.Local != string(clusterseal.LocalServing) || cs.At == "" {
		t.Fatalf("ClusterSeal = %+v", cs)
	}
}

// TestUnixRT_BarrierHolders_ServiceErrors: a barrier service failure on each
// holder RPC is a typed RequestError naming that RPC.
func TestUnixRT_BarrierHolders_ServiceErrors(t *testing.T) {
	svc := &holderBarrierSvc{err: errors.New("ceremony store unavailable")}
	tr, cleanup := startMTLSServer(t, unixserver.WithBarrierService(svc))
	defer cleanup()
	ctx := context.Background()

	reg := &transport.OfficerRegisterRequest{Username: "alice", CSRPEM: "csr"}
	begin := &transport.ShareClaimBeginRequest{Username: "alice"}
	complete := &transport.ShareClaimCompleteRequest{Username: "alice", NonceHex: "beef"}
	calls := map[string]func() error{
		"OfficerRegister":            func() error { _, err := tr.OfficerRegister(ctx, reg); return err },
		"RecoveryOfficerRegister":    func() error { _, err := tr.RecoveryOfficerRegister(ctx, reg); return err },
		"OfficerList":                func() error { _, err := tr.OfficerList(ctx); return err },
		"ShareClaimBegin":            func() error { _, err := tr.ShareClaimBegin(ctx, begin); return err },
		"ShareClaimComplete":         func() error { _, err := tr.ShareClaimComplete(ctx, complete); return err },
		"RecoveryShareClaimBegin":    func() error { _, err := tr.RecoveryShareClaimBegin(ctx, begin); return err },
		"RecoveryShareClaimComplete": func() error { _, err := tr.RecoveryShareClaimComplete(ctx, complete); return err },
		"BarrierQuorumBegin": func() error {
			_, err := tr.BarrierQuorumBegin(ctx, &transport.QuorumBeginRequest{Domain: string(seal.DomainUnseal), Op: string(seal.OpUnseal)})
			return err
		},
		"BarrierClusterUnseal": func() error { _, err := tr.BarrierClusterUnseal(ctx); return err },
	}
	for method, call := range calls {
		requireUnixRequestError(t, call(), method)
	}
}

func TestUnixRT_BarrierHolders_Closed(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithBarrierService(&holderBarrierSvc{}))
	cleanup()
	ctx := context.Background()

	_, err := tr.OfficerRegister(ctx, &transport.OfficerRegisterRequest{Username: "alice"})
	assertClosed(t, err)
	_, err = tr.RecoveryOfficerRegister(ctx, &transport.OfficerRegisterRequest{Username: "alice"})
	assertClosed(t, err)
	_, err = tr.OfficerList(ctx)
	assertClosed(t, err)
	_, err = tr.ShareClaimBegin(ctx, &transport.ShareClaimBeginRequest{Username: "alice"})
	assertClosed(t, err)
	_, err = tr.ShareClaimComplete(ctx, &transport.ShareClaimCompleteRequest{Username: "alice"})
	assertClosed(t, err)
	_, err = tr.RecoveryShareClaimBegin(ctx, &transport.ShareClaimBeginRequest{Username: "alice"})
	assertClosed(t, err)
	_, err = tr.RecoveryShareClaimComplete(ctx, &transport.ShareClaimCompleteRequest{Username: "alice"})
	assertClosed(t, err)
	_, err = tr.BarrierQuorumBegin(ctx, &transport.QuorumBeginRequest{})
	assertClosed(t, err)
	_, err = tr.BarrierClusterUnseal(ctx)
	assertClosed(t, err)
}

// TestUnixRT_BarrierHolders_NilRequest proves every barrier holder method
// refuses a nil request with a typed error instead of dereferencing it, and
// that nothing reaches the barrier service.
func TestUnixRT_BarrierHolders_NilRequest(t *testing.T) {
	svc := &holderBarrierSvc{}
	tr, cleanup := startAuthServer(t, unixserver.WithBarrierService(svc))
	defer cleanup()
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
		"BarrierRekeyStart":          func() error { _, err := tr.BarrierRekeyStart(ctx, nil); return err },
		"BarrierOfficerRevoke":       func() error { _, err := tr.BarrierOfficerRevoke(ctx, nil); return err },
	}
	for method, call := range calls {
		err := call()
		requireUnixRequestError(t, err, method)
		var nilReq *transport.NilRequestError
		if !errors.As(err, &nilReq) {
			t.Errorf("%s: err = %v, want *transport.NilRequestError", method, err)
		}
	}
	if svc.register != nil || svc.begin != nil || svc.username != "" {
		t.Fatal("a nil request reached the barrier service")
	}
}
