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

package tenant

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony/ceremonytest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// tenantHolder is one tenant share holder's client side.
type tenantHolder struct {
	name     string
	key      *ecdsa.PrivateKey
	csr      []byte
	thumb    string
	envelope []byte
}

func newTenantHolder(t *testing.T, name string) *tenantHolder {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "admin"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tenantHolder{name: name, key: key, csr: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})}
}

// newTenantHolderCA builds a LocalCA over a fresh self-signed CA.
func newTenantHolderCA(t *testing.T) *ceremony.LocalCA {
	t.Helper()
	ca, _ := ceremonytest.NewCA(t, "Tenant Holder CA")
	return ca
}

// tenantAdminCtx is a context authenticated as a tenant admin of tenantID.
func tenantAdminCtx(tenantID string) context.Context {
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "tenant-admin", TenantID: tenantID})
	return WithTenantID(ctx, tenantID)
}

// newHolderTenantService builds a tenant service with holder deps wired.
func newHolderTenantService(t *testing.T) *TenantService {
	t.Helper()
	svc, err := NewTenantService(seal.NewMemoryBackend(), newTestBarrierRegistry(t), testLogger(),
		WithTenantHolderDeps(ceremony.HolderDeps{CA: newTenantHolderCA(t), TrustDomain: "db.example.com"}))
	if err != nil {
		t.Fatalf("NewTenantService: %v", err)
	}
	return svc
}

// submitTenant answers the mtls factor and submits h's share to op.
func submitTenant(t *testing.T, svc *TenantService, ctx context.Context, tenantID string, op seal.QuorumOp, h *tenantHolder) (*seal.QuorumOutcome, error) {
	t.Helper()
	ch, err := svc.TenantBarrierQuorumBegin(ctx, tenantID, op, h.name, "")
	if err != nil {
		return nil, err
	}
	sig, err := mtls.SignNonce(h.key, ch.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	env, err := ceremony.ParseShareEnvelope(h.envelope)
	if err != nil {
		t.Fatal(err)
	}
	share, err := env.OpenWithKey(h.key)
	if err != nil {
		t.Fatal(err)
	}
	return svc.TenantBarrierQuorumSubmit(ctx, tenantID, &seal.HolderSubmission{
		Op: op, Holder: h.name, Share: share,
		Proof: seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: sig},
	})
}

// TestTenantBarrierHolders_Lifecycle drives a tenant barrier's holders end
// to end: enrolment before initialization, the Shamir split, claims, a
// quorum seal and unseal, and revocation's quorum check.
func TestTenantBarrierHolders_Lifecycle(t *testing.T) {
	const tenantID = "acme"
	svc := newHolderTenantService(t)
	ctx := tenantAdminCtx(tenantID)
	if _, err := svc.CreateTenant(context.Background(), tenantID, "Acme", ""); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}

	holders := []*tenantHolder{newTenantHolder(t, "alice"), newTenantHolder(t, "bob")}
	for _, h := range holders {
		res, err := svc.TenantBarrierOfficerRegister(ctx, tenantID, &ceremony.RegisterOfficerRequest{Username: h.name, Role: seal.RoleCustodian, CSRPEM: h.csr})
		if err != nil {
			t.Fatalf("register %s: %v", h.name, err)
		}
		cert, err := mtls.ParseCertificatePEM(res.CertPEM)
		if err != nil {
			t.Fatal(err)
		}
		h.thumb = mtls.Thumbprint(cert.Raw)
	}
	if _, err := svc.TenantBarrierOfficerRegister(tenantAdminCtx(tenantID), tenantID, nil); err == nil {
		t.Fatal("a nil registration must be refused")
	}

	res, err := svc.TenantBarrierInitWithStrategy(ctx, tenantID, nil, 2, 2, seal.StrategyShamir)
	if err != nil {
		t.Fatalf("init shamir: %v", err)
	}
	if res.ShareCount != 2 {
		t.Fatalf("ShareCount = %d, want 2", res.ShareCount)
	}

	list, err := svc.TenantBarrierOfficerList(ctx, tenantID)
	if err != nil || len(list) != 2 {
		t.Fatalf("OfficerList = %v, %v", list, err)
	}
	for _, h := range holders {
		ch, err := svc.TenantBarrierShareClaimBegin(ctx, tenantID, h.name, h.thumb)
		if err != nil {
			t.Fatalf("claim begin %s: %v", h.name, err)
		}
		sig, err := mtls.SignNonce(h.key, ch.Nonce)
		if err != nil {
			t.Fatal(err)
		}
		cr, err := svc.TenantBarrierShareClaimComplete(ctx, tenantID, h.name, hex.EncodeToString(ch.Nonce), sig, h.thumb)
		if err != nil {
			t.Fatalf("claim complete %s: %v", h.name, err)
		}
		h.envelope = cr.Envelope
	}

	if _, err := submitTenant(t, svc, ctx, tenantID, seal.OpSeal, holders[0]); err != nil {
		t.Fatalf("seal vote 1: %v", err)
	}
	out, err := submitTenant(t, svc, ctx, tenantID, seal.OpSeal, holders[1])
	if err != nil || !out.Complete {
		t.Fatalf("seal vote 2 = %+v, %v", out, err)
	}
	st, err := svc.TenantBarrierStatus(ctx, tenantID)
	if err != nil || !st.Sealed {
		t.Fatalf("status after seal quorum = %+v, %v", st, err)
	}

	if _, err := submitTenant(t, svc, ctx, tenantID, seal.OpUnseal, holders[1]); err != nil {
		t.Fatalf("unseal 1: %v", err)
	}
	out, err = submitTenant(t, svc, ctx, tenantID, seal.OpUnseal, holders[0])
	if err != nil || !out.Complete {
		t.Fatalf("unseal 2 = %+v, %v", out, err)
	}

	_, err = svc.TenantBarrierOfficerRevoke(ctx, tenantID, "bob", 0)
	var breaks *seal.RevocationBreaksQuorumError
	if !errors.As(err, &breaks) {
		t.Fatalf("revoking below the threshold: got %v, want RevocationBreaksQuorumError", err)
	}
	round, err := svc.TenantBarrierRekeyStart(ctx, tenantID, 2, seal.FactorPolicyAnyEnrolled)
	if err != nil {
		t.Fatalf("rekey start: %v", err)
	}
	if round.Op != seal.OpRekey || round.NewThreshold != 2 {
		t.Fatalf("round = %+v", round)
	}
}

// TestTenantBarrierHolders_Refusals covers the shared argument, scope and
// configuration checks of every holder operation.
func TestTenantBarrierHolders_Refusals(t *testing.T) {
	svc := newHolderTenantService(t)
	if _, err := svc.CreateTenant(context.Background(), "victim", "Victim", ""); err != nil {
		t.Fatal(err)
	}
	attacker := tenantAdminCtx("attacker")
	own := tenantAdminCtx("victim")

	ops := map[string]func(ctx context.Context, id string) error{
		"register": func(ctx context.Context, id string) error {
			_, err := svc.TenantBarrierOfficerRegister(ctx, id, &ceremony.RegisterOfficerRequest{Username: "a"})
			return err
		},
		"list": func(ctx context.Context, id string) error {
			_, err := svc.TenantBarrierOfficerList(ctx, id)
			return err
		},
		"claim begin": func(ctx context.Context, id string) error {
			_, err := svc.TenantBarrierShareClaimBegin(ctx, id, "a", "t")
			return err
		},
		"claim complete": func(ctx context.Context, id string) error {
			_, err := svc.TenantBarrierShareClaimComplete(ctx, id, "a", "n", nil, "t")
			return err
		},
		"quorum begin": func(ctx context.Context, id string) error {
			_, err := svc.TenantBarrierQuorumBegin(ctx, id, seal.OpUnseal, "a", "")
			return err
		},
		"quorum submit": func(ctx context.Context, id string) error {
			_, err := svc.TenantBarrierQuorumSubmit(ctx, id, &seal.HolderSubmission{Op: seal.OpUnseal, Holder: "a", Share: []byte("s")})
			return err
		},
		"rekey": func(ctx context.Context, id string) error {
			_, err := svc.TenantBarrierRekeyStart(ctx, id, 2, "")
			return err
		},
		"revoke": func(ctx context.Context, id string) error {
			_, err := svc.TenantBarrierOfficerRevoke(ctx, id, "a", 0)
			return err
		},
	}
	for name, op := range ops {
		if err := op(own, ""); !errors.Is(err, ErrEmptyTenantID) {
			t.Errorf("%s with empty tenant: got %v", name, err)
		}
		var tse *TenantScopeError
		if err := op(attacker, "victim"); !errors.As(err, &tse) {
			t.Errorf("%s across tenants: got %v, want TenantScopeError", name, err)
		}
		if err := op(tenantAdminCtx("ghost"), "ghost"); !errors.Is(err, ErrTenantNotFound) {
			t.Errorf("%s on an unknown tenant: got %v", name, err)
		}
	}

	// Without holder dependencies there is no holder authority.
	bare, _ := newTestService(t)
	if _, err := bare.CreateTenant(context.Background(), "victim", "Victim", ""); err != nil {
		t.Fatal(err)
	}
	_, err := bare.TenantBarrierOfficerList(own, "victim")
	if !errors.Is(err, seal.ErrCeremonyDistributorNotConfigured) {
		t.Errorf("no holder deps: got %v", err)
	}
	if _, err := svc.TenantBarrierQuorumSubmit(own, "victim", nil); err == nil {
		t.Error("a nil submission must be refused")
	}
}
