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

package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// TestOfficerBinding_SystemLifecycle drives the desktop holder binding
// against a real barrier: enrolment, listing, a submission that opens a real
// envelope, rekey and revocation. An embedded connection carries no TLS
// certificate, so the claim is in process: bound to the holder's enrolled
// certificate and released only for the nonce signature by its key.
func TestOfficerBinding_SystemLifecycle(t *testing.T) {
	stack := newCLIHolderStack(t, 2, 3)
	conn := newTestConnectionBinding(t, newMockKVService(), newMockAdminService(),
		embedded.WithBarrierService(stack.svc), embedded.WithPINBackend(pintest.NewBackend(t)))
	b := NewOfficerBinding(testLogger(t), conn)

	holders := []*cliHolder{
		newCLIHolder(t, "alice", transport.RoleSecurityOfficer, false, false),
		newCLIHolder(t, "bob", transport.RoleSecurityOfficer, false, false),
		newCLIHolder(t, "carol", transport.RoleCustodian, true, false),
	}
	for _, h := range holders {
		csr, err := os.ReadFile(h.csrFile)
		if err != nil {
			t.Fatal(err)
		}
		req := &transport.OfficerRegisterRequest{Username: h.name, Role: h.role, CSRPEM: string(csr)}
		if h.encPubFile != "" {
			if req.EncryptionKey, err = os.ReadFile(h.encPubFile); err != nil {
				t.Fatal(err)
			}
			req.EncryptionKEM = keydelivery.KEMX25519
		}
		resp, err := b.RegisterHolder(HolderScope{}, req)
		if err != nil {
			t.Fatalf("RegisterHolder(%s): %v", h.name, err)
		}
		if h.cert, err = mtls.ParseCertificatePEM([]byte(resp.CertPEM)); err != nil {
			t.Fatal(err)
		}
	}
	// A plain, non-ceremony BarrierService over the same barrier:
	// stack.svc's ceremony now refuses BarrierInitializeShamir
	// (go-qrdb#560 review item 8), the same way it already refused
	// BarrierInitialize.
	if _, err := service.NewBarrierService(stack.barrier).BarrierInitializeShamir(context.Background(), 2, 3, ""); err != nil {
		t.Fatal(err)
	}
	list, err := b.ListHolders(HolderScope{Domain: transport.DomainUnseal})
	if err != nil || len(list) != 3 {
		t.Fatalf("ListHolders = %d holders, %v", len(list), err)
	}

	alice, carol := holders[0], holders[2]
	aliceKey, err := os.ReadFile(alice.keyFile)
	if err != nil {
		t.Fatal(err)
	}
	bobKey, err := os.ReadFile(holders[1].keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ClaimEnvelope(&HolderClaimParams{Username: alice.name, KeyPEM: string(bobKey)}); !errors.Is(err, ceremony.ErrChallengeVerificationFailed) {
		t.Fatalf("a claim signed by another holder's key = %v, want ErrChallengeVerificationFailed", err)
	}
	claimed, err := b.ClaimEnvelope(&HolderClaimParams{Username: alice.name, KeyPEM: string(aliceKey)})
	if err != nil || len(claimed.Envelope) == 0 {
		t.Fatalf("ClaimEnvelope over an embedded profile = %+v, %v", claimed, err)
	}
	if _, err := b.ClaimEnvelope(&HolderClaimParams{Username: alice.name, KeyPEM: string(aliceKey)}); !errors.Is(err, ceremony.ErrNoPendingShare) {
		t.Fatalf("a second claim = %v, want ErrNoPendingShare", err)
	}
	envelopes := map[*cliHolder][]byte{alice: claimed.Envelope, carol: serviceClaim(t, stack.svc, carol, carol.cert)}

	round, err := b.StartRekey(&HolderRekeyParams{Threshold: 2, SOPin: cliSOPIN})
	if err != nil || round.Op != transport.OpRekey {
		t.Fatalf("StartRekey = %+v, %v", round, err)
	}
	carolKey, err := os.ReadFile(carol.keyFile)
	if err != nil {
		t.Fatal(err)
	}
	carolEnc, err := os.ReadFile(carol.encPrvFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []struct {
		holder *cliHolder
		key    []byte
		enc    []byte
	}{{alice, aliceKey, nil}, {carol, carolKey, carolEnc}} {
		out, err := b.SubmitShare(&HolderSubmitParams{Op: transport.OpRekey, Username: h.holder.name,
			Envelope: string(envelopes[h.holder]), KeyPEM: string(h.key), EncryptionKey: h.enc})
		if err != nil {
			t.Fatalf("SubmitShare(%s): %v", h.holder.name, err)
		}
		if h.holder == carol && !out.Complete {
			t.Fatalf("rekey did not complete at the threshold: %+v", out)
		}
	}

	round, err = b.RevokeHolder(&HolderRekeyParams{Username: "bob", Threshold: 2, SOPin: cliSOPIN})
	if err != nil || len(round.Remove) != 1 || round.Remove[0] != "bob" {
		t.Fatalf("RevokeHolder = %+v, %v", round, err)
	}
}

// TestOfficerBinding_Refusals covers the binding's own checks: no
// connection, an unknown domain, and a key that is not PEM.
func TestOfficerBinding_Refusals(t *testing.T) {
	unconnected := NewOfficerBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	if _, err := unconnected.ListHolders(HolderScope{}); err == nil {
		t.Fatal("ListHolders answered without a connection")
	}
	for name, call := range map[string]func(*OfficerBinding) error{
		"register": func(b *OfficerBinding) error {
			_, err := b.RegisterHolder(HolderScope{}, &transport.OfficerRegisterRequest{})
			return err
		},
		"claim": func(b *OfficerBinding) error {
			_, err := b.ClaimEnvelope(&HolderClaimParams{})
			return err
		},
		"submit": func(b *OfficerBinding) error {
			_, err := b.SubmitShare(&HolderSubmitParams{})
			return err
		},
		"rekey": func(b *OfficerBinding) error {
			_, err := b.StartRekey(&HolderRekeyParams{})
			return err
		},
		"revoke": func(b *OfficerBinding) error {
			_, err := b.RevokeHolder(&HolderRekeyParams{})
			return err
		},
	} {
		t.Run(name+"/unconnected", func(t *testing.T) {
			if call(unconnected) == nil {
				t.Fatal("answered without a connection")
			}
		})
	}

	stack := newCLIHolderStack(t, 2, 3)
	b := NewOfficerBinding(testLogger(t), newTestConnectionBinding(t, newMockKVService(), newMockAdminService(),
		embedded.WithBarrierService(stack.svc)))
	var invalid *InvalidArgError
	if _, err := b.ListHolders(HolderScope{Domain: "bogus"}); !errors.As(err, &invalid) {
		t.Fatalf("unknown domain: %v", err)
	}
	if _, err := b.ClaimEnvelope(&HolderClaimParams{Username: "alice", KeyPEM: "not pem"}); err == nil {
		t.Fatal("a claim with an unparseable key was attempted")
	}
	if _, err := b.SubmitShare(&HolderSubmitParams{Op: transport.OpUnseal, Username: "alice", KeyPEM: "not pem"}); err == nil {
		t.Fatal("a submission with an unparseable key was attempted")
	}
}

// TestOfficerBinding_TenantScope routes a tenant scope to the tenant
// service.
func TestOfficerBinding_TenantScope(t *testing.T) {
	var listed string
	svc := &mockTenantService{
		officerListFn: func(_ context.Context, id string) ([]*ceremony.OfficerStatus, error) {
			listed = id
			return []*ceremony.OfficerStatus{{Username: "carol", ClaimedAt: time.Now()}}, nil
		},
		rekeyStartFn: func(context.Context, string, int, seal.FactorPolicy) (*seal.QuorumRoundInfo, error) {
			return &seal.QuorumRoundInfo{Op: seal.OpRekey, Required: 2}, nil
		},
	}
	b := NewOfficerBinding(testLogger(t), newTestConnectionBinding(t, newMockKVService(), newMockAdminService(),
		embedded.WithTenantService(svc)))
	list, err := b.ListHolders(HolderScope{TenantID: "acme"})
	if err != nil || len(list) != 1 || listed != "acme" {
		t.Fatalf("tenant ListHolders = %v, %v (tenant %q)", list, err, listed)
	}
	if _, err := b.StartRekey(&HolderRekeyParams{HolderScope: HolderScope{TenantID: "acme"}, Threshold: 2}); err != nil {
		t.Fatalf("tenant StartRekey: %v", err)
	}
}
