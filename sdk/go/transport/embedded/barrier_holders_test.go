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

package embedded

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-objstore/pkg/common"
	"github.com/jeremyhahn/go-objstore/pkg/local"
	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony/ceremonytest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/servicetest"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// newSystemHolderTransport is the embedded transport over the REAL barrier
// service and ceremony of a 2-of-2 Shamir system barrier whose holders are
// not yet enrolled, with the service so a test can initialize it.
func newSystemHolderTransport(t *testing.T) (*Transport, service.BarrierServicer) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	strategy, err := seal.NewShamirStrategy(2, 2)
	if err != nil {
		t.Fatal(err)
	}
	barrier, err := seal.NewStorageBarrier(logger, seal.NewMemoryBackend(), seal.BarrierConfig{
		RootKeyPath: "root-key",
		Shamir:      &seal.ShamirConfig{Threshold: 2, TotalShares: 2},
	}, strategy)
	if err != nil {
		t.Fatal(err)
	}
	core, err := ceremony.NewCeremonyService(&ceremony.CeremonyConfig{DataDir: t.TempDir()}, barrier, logger)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := ceremonytest.NewCA(t, "Embedded Holder CA")
	core.SetCA(ca)
	svc := service.NewBarrierServiceWithCeremony(barrier, core)
	return newExtTransport(t, WithBarrierService(svc)), svc
}

// holderAdminCtx is a context authenticated as the enrolling admin.
func holderAdminCtx() context.Context {
	return auth.WithIdentity(context.Background(), &auth.Identity{Subject: "admin"})
}

// claimSystemShare claims h's share of domain through tr in process: the
// claim is bound to the enrolled certificate by h's signature over the nonce.
func claimSystemShare(t *testing.T, tr *Transport, domain seal.QuorumDomain, h *servicetest.Holder) {
	t.Helper()
	ctx := context.Background()
	begin, complete := tr.ShareClaimBegin, tr.ShareClaimComplete
	label := ceremony.SystemClaimLabel
	if domain == seal.DomainRecovery {
		begin, complete = tr.RecoveryShareClaimBegin, tr.RecoveryShareClaimComplete
		label = ceremony.RecoveryClaimLabel
	}
	ch, err := begin(ctx, &transport.ShareClaimBeginRequest{Username: h.Name})
	if err != nil {
		t.Fatalf("claim begin %s: %v", h.Name, err)
	}
	nonce, err := hex.DecodeString(ch.NonceHex)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := ceremony.SignShareClaim(h.Key, label, h.Name, nonce)
	if err != nil {
		t.Fatal(err)
	}
	done, err := complete(ctx, &transport.ShareClaimCompleteRequest{Username: h.Name, NonceHex: ch.NonceHex, Signature: sig})
	if err != nil {
		t.Fatalf("claim complete %s: %v", h.Name, err)
	}
	h.Envelope = done.Envelope
}

// submitSystemShare answers h's mtls challenge for op through tr and submits
// its share.
func submitSystemShare(t *testing.T, tr *Transport, op string, h *servicetest.Holder) (*transport.QuorumOutcome, error) {
	t.Helper()
	ctx := context.Background()
	ch, err := tr.BarrierQuorumBegin(ctx, &transport.QuorumBeginRequest{Domain: transport.DomainUnseal, Op: op, Username: h.Name})
	if err != nil {
		return nil, err
	}
	if ch.ChallengeID == "" || ch.Factor == "" || len(ch.Nonce) == 0 {
		t.Fatalf("challenge = %+v, want an ID, a factor and a nonce", ch)
	}
	return tr.BarrierQuorumSubmit(ctx, &transport.QuorumSubmitRequest{
		Domain: transport.DomainUnseal, Op: op, Username: h.Name, ChallengeID: ch.ChallengeID, Factor: ch.Factor,
		Response: h.Sign(t, ch.ChallengeID, ch.MethodData[mtls.MethodDataPurpose], ch.Nonce), Share: h.Share(t),
	})
}

// TestTransport_SystemHolders drives the system barrier's holder verbs
// through the embedded transport over the real ceremony: enrolment of the
// unseal-key holders and of a recovery-key holder, listing, each holder's
// claim, and a root-token quorum that completes only on the second share.
func TestTransport_SystemHolders(t *testing.T) {
	tr, svc := newSystemHolderTransport(t)
	holders := []*servicetest.Holder{servicetest.NewHolder(t, "alice"), servicetest.NewHolder(t, "bob")}
	for _, h := range holders {
		resp, err := tr.OfficerRegister(holderAdminCtx(), &transport.OfficerRegisterRequest{Username: h.Name, CSRPEM: h.CSRPEM})
		if err != nil {
			t.Fatalf("register %s: %v", h.Name, err)
		}
		h.Enrolled(t, resp.CertPEM)
	}
	rec := servicetest.NewHolder(t, "rita")
	recResp, err := tr.RecoveryOfficerRegister(holderAdminCtx(), &transport.OfficerRegisterRequest{Username: rec.Name, CSRPEM: rec.CSRPEM})
	if err != nil {
		t.Fatalf("register recovery holder: %v", err)
	}
	rec.Enrolled(t, recResp.CertPEM)

	officers, err := tr.OfficerList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(officers) != 2 {
		t.Fatalf("listed %d unseal-key holders, want 2: the recovery holder is not one", len(officers))
	}
	for _, o := range officers {
		if o.Username != "alice" && o.Username != "bob" {
			t.Fatalf("listed holder %q", o.Username)
		}
		if o.CertThumbprint == "" {
			t.Fatalf("holder %q listed without its certificate thumbprint", o.Username)
		}
	}

	if _, err := svc.BarrierInitializeShamir(context.Background(), 2, 2, ""); err != nil {
		t.Fatal(err)
	}
	for _, h := range holders {
		claimSystemShare(t, tr, seal.DomainUnseal, h)
	}

	out, err := submitSystemShare(t, tr, transport.OpRootToken, holders[0])
	if err != nil {
		t.Fatalf("first root-token share: %v", err)
	}
	if out.Complete || out.Required != 2 || out.Submitted != 1 || out.RootToken != "" {
		t.Fatalf("after one of two shares = %+v, want an incomplete round and no token", out)
	}
	out, err = submitSystemShare(t, tr, transport.OpRootToken, holders[1])
	if err != nil {
		t.Fatalf("second root-token share: %v", err)
	}
	if !out.Complete || out.RootToken == "" {
		t.Fatalf("after two of two shares = %+v, want a completed round with a root token", out)
	}
}

// TestTransport_SystemHolders_ServiceRefusals verifies the service's
// refusals come back through each verb rather than a result: a duplicate
// enrolment, a challenge for an op the domain does not have, a share
// submitted on the decoy challenge an unenrolled holder is issued, and a
// submission whose factor answer is not the holder's.
func TestTransport_SystemHolders_ServiceRefusals(t *testing.T) {
	tr, svc := newSystemHolderTransport(t)
	alice, bob := servicetest.NewHolder(t, "alice"), servicetest.NewHolder(t, "bob")
	for _, h := range []*servicetest.Holder{alice, bob} {
		resp, err := tr.OfficerRegister(holderAdminCtx(), &transport.OfficerRegisterRequest{Username: h.Name, CSRPEM: h.CSRPEM})
		if err != nil {
			t.Fatal(err)
		}
		h.Enrolled(t, resp.CertPEM)
	}
	if _, err := tr.OfficerRegister(holderAdminCtx(), &transport.OfficerRegisterRequest{Username: "alice", CSRPEM: alice.CSRPEM}); err == nil {
		t.Fatal("a second enrolment of alice succeeded")
	}
	if _, err := svc.BarrierInitializeShamir(context.Background(), 2, 2, ""); err != nil {
		t.Fatal(err)
	}
	claimSystemShare(t, tr, seal.DomainUnseal, alice)

	ctx := context.Background()
	_, err := tr.BarrierQuorumBegin(ctx, &transport.QuorumBeginRequest{Domain: transport.DomainUnseal, Op: transport.OpRecover, Username: "alice"})
	var badOp *seal.QuorumOpInvalidError
	if !errors.As(err, &badOp) {
		t.Fatalf("a recovery op in the unseal domain = %v, want QuorumOpInvalidError", err)
	}
	// A holder nobody enrolled is issued a decoy challenge, so a challenge
	// does not reveal who holds a share; a share submitted on it is refused.
	decoy, err := tr.BarrierQuorumBegin(ctx, &transport.QuorumBeginRequest{Domain: transport.DomainUnseal, Op: transport.OpRootToken, Username: "mallory"})
	if err != nil {
		t.Fatalf("a challenge for an unenrolled holder = %v, want a decoy", err)
	}
	if out, err := tr.BarrierQuorumSubmit(ctx, &transport.QuorumSubmitRequest{
		Domain: transport.DomainUnseal, Op: transport.OpRootToken, Username: "mallory",
		ChallengeID: decoy.ChallengeID, Factor: decoy.Factor, Response: bob.Sign(t, decoy.ChallengeID, decoy.MethodData[mtls.MethodDataPurpose], decoy.Nonce), Share: alice.Share(t),
	}); err == nil {
		t.Fatalf("a share submitted on a decoy challenge was counted: %+v", out)
	}
	ch, err := tr.BarrierQuorumBegin(ctx, &transport.QuorumBeginRequest{Domain: transport.DomainUnseal, Op: transport.OpRootToken, Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	// Bob's key answers alice's challenge: the proof is not alice's.
	forged := bob.Sign(t, ch.ChallengeID, ch.MethodData[mtls.MethodDataPurpose], ch.Nonce)
	out, err := tr.BarrierQuorumSubmit(ctx, &transport.QuorumSubmitRequest{
		Domain: transport.DomainUnseal, Op: transport.OpRootToken, Username: "alice",
		ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: forged, Share: alice.Share(t),
	})
	if err == nil {
		t.Fatalf("a share submitted under another holder's factor answer was counted: %+v", out)
	}
}

// TestTransport_SystemHolders_Unconfigured verifies every system holder verb
// refuses, without dereferencing, on a transport with no barrier service and
// on a closed one.
func TestTransport_SystemHolders_Unconfigured(t *testing.T) {
	ctx := context.Background()
	calls := func(tr *Transport) map[string]func() error {
		reg := &transport.OfficerRegisterRequest{Username: "alice"}
		return map[string]func() error{
			"OfficerRegister":         func() error { _, err := tr.OfficerRegister(ctx, reg); return err },
			"RecoveryOfficerRegister": func() error { _, err := tr.RecoveryOfficerRegister(ctx, reg); return err },
			"OfficerList":             func() error { _, err := tr.OfficerList(ctx); return err },
			"BarrierQuorumBegin": func() error {
				_, err := tr.BarrierQuorumBegin(ctx, &transport.QuorumBeginRequest{Username: "alice"})
				return err
			},
			"BarrierQuorumSubmit": func() error {
				_, err := tr.BarrierQuorumSubmit(ctx, &transport.QuorumSubmitRequest{Username: "alice"})
				return err
			},
			"BarrierOfficerRevoke": func() error {
				_, err := tr.BarrierOfficerRevoke(ctx, &transport.OfficerRevokeRequest{Username: "alice"})
				return err
			},
		}
	}
	for name, call := range calls(newExtTransport(t)) {
		var nilSvc *NilServiceError
		if err := call(); !errors.As(err, &nilSvc) || nilSvc.Service != "BarrierServicer" {
			t.Errorf("%s with no barrier service = %v, want NilServiceError{BarrierServicer}", name, err)
		}
	}
	_, svc := newSystemHolderTransport(t)
	for name, call := range calls(closedExtTransport(t, WithBarrierService(svc))) {
		var closed *transport.ClosedError
		if err := call(); !errors.As(err, &closed) {
			t.Errorf("%s on a closed transport = %v, want ClosedError", name, err)
		}
	}
}

// tenantHolderTenant is the tenant whose barrier the tenant holder tests
// drive.
const tenantHolderTenant = "acme"

// newTenantHolderTransport is the embedded transport over a REAL tenant
// service with holder custody wired, holding tenantHolderTenant, whose
// barrier is not yet initialized.
func newTenantHolderTransport(t *testing.T) (*Transport, *tenant.TenantService) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// A barrier-isolated tenant lives on a volume of the pool, in its own
	// Pebble, both under t-scoped temporary directories.
	vol := local.New()
	if err := vol.Configure(map[string]string{"path": t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	state := newMemStateStore()
	pools := objpool.NewManager()
	if err := pools.Register("default", []common.Storage{vol}, &objpool.RoundRobinStrategy{State: state}); err != nil {
		t.Fatal(err)
	}
	perTenant := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenant.CloseAll(context.Background()) })
	reg, err := seal.NewBarrierRegistry(logger, sealtest.UnsealedBarrier(t),
		seal.WithVolumePool(pools, state, "default"), seal.WithPerTenantRegistry(perTenant))
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := ceremonytest.NewCA(t, "Embedded Tenant Holder CA")
	svc, err := tenant.NewTenantService(seal.NewMemoryBackend(), reg, logger, tenant.WithTenantHolderDeps(ceremony.HolderDeps{CA: ca}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateTenant(context.Background(), tenantHolderTenant, "Acme", ""); err != nil {
		t.Fatal(err)
	}
	return newExtTransport(t, WithTenantService(svc)), svc
}

// tenantHolderCtx is a context authenticated as tenantHolderTenant's admin.
func tenantHolderCtx() context.Context {
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "tenant-admin", TenantID: tenantHolderTenant})
	return tenant.WithTenantID(ctx, tenantHolderTenant)
}

// submitTenantShare answers h's mtls challenge for op through tr and submits
// its share.
func submitTenantShare(t *testing.T, tr *Transport, op string, h *servicetest.Holder) (*transport.QuorumOutcome, error) {
	t.Helper()
	ctx := tenantHolderCtx()
	ch, err := tr.TenantBarrierQuorumBegin(ctx, &transport.TenantQuorumBeginRequest{TenantID: tenantHolderTenant, Op: op, Username: h.Name})
	if err != nil {
		return nil, err
	}
	return tr.TenantBarrierQuorumSubmit(ctx, &transport.TenantQuorumSubmitRequest{
		TenantID: tenantHolderTenant, Op: op, Username: h.Name, ChallengeID: ch.ChallengeID, Factor: ch.Factor,
		Response: h.Sign(t, ch.ChallengeID, ch.MethodData[mtls.MethodDataPurpose], ch.Nonce), Share: h.Share(t),
	})
}

// TestTransport_TenantHolders drives a tenant barrier's holder verbs through
// the embedded transport over the real tenant service: enrolment, listing,
// claims, a seal and an unseal quorum, a rekey round, and a revocation the
// remaining holder cannot satisfy.
func TestTransport_TenantHolders(t *testing.T) {
	tr, svc := newTenantHolderTransport(t)
	ctx := tenantHolderCtx()
	holders := []*servicetest.Holder{servicetest.NewHolder(t, "alice"), servicetest.NewHolder(t, "bob")}
	for _, h := range holders {
		resp, err := tr.TenantBarrierOfficerRegister(ctx, &transport.TenantOfficerRegisterRequest{
			TenantID: tenantHolderTenant, Username: h.Name, Role: string(seal.RoleCustodian), CSRPEM: h.CSRPEM,
		})
		if err != nil {
			t.Fatalf("register %s: %v", h.Name, err)
		}
		h.Enrolled(t, resp.CertPEM)
	}
	if _, err := svc.TenantBarrierInitWithStrategy(ctx, tenantHolderTenant, nil, 2, 2, seal.StrategyShamir); err != nil {
		t.Fatal(err)
	}
	officers, err := tr.TenantBarrierOfficerList(ctx, tenantHolderTenant)
	if err != nil {
		t.Fatal(err)
	}
	if len(officers) != 2 {
		t.Fatalf("listed %d holders, want 2", len(officers))
	}
	for _, h := range holders {
		ch, err := tr.TenantBarrierShareClaimBegin(ctx, &transport.TenantShareClaimBeginRequest{TenantID: tenantHolderTenant, Username: h.Name})
		if err != nil {
			t.Fatalf("claim begin %s: %v", h.Name, err)
		}
		nonce, err := hex.DecodeString(ch.NonceHex)
		if err != nil {
			t.Fatal(err)
		}
		sig, err := ceremony.SignShareClaim(h.Key, ceremony.TenantClaimLabel(tenantHolderTenant), h.Name, nonce)
		if err != nil {
			t.Fatal(err)
		}
		done, err := tr.TenantBarrierShareClaimComplete(ctx, &transport.TenantShareClaimCompleteRequest{
			TenantID: tenantHolderTenant, Username: h.Name, NonceHex: ch.NonceHex, Signature: sig,
		})
		if err != nil {
			t.Fatalf("claim complete %s: %v", h.Name, err)
		}
		h.Envelope = done.Envelope
	}

	for _, op := range []string{transport.OpSeal, transport.OpUnseal} {
		var out *transport.QuorumOutcome
		for _, h := range holders {
			if out, err = submitTenantShare(t, tr, op, h); err != nil {
				t.Fatalf("%s share of %s: %v", op, h.Name, err)
			}
		}
		if !out.Complete {
			t.Fatalf("%s quorum = %+v, want complete", op, out)
		}
		st, err := svc.TenantBarrierStatus(ctx, tenantHolderTenant)
		if err != nil {
			t.Fatal(err)
		}
		if st.Sealed != (op == transport.OpSeal) {
			t.Fatalf("after the %s quorum the barrier reads sealed=%v", op, st.Sealed)
		}
	}

	round, err := tr.TenantBarrierRekeyStart(ctx, &transport.TenantRekeyStartRequest{TenantID: tenantHolderTenant, Threshold: 2, Policy: string(seal.FactorPolicyAnyEnrolled)})
	if err != nil {
		t.Fatalf("rekey start: %v", err)
	}
	if round.Op != transport.OpRekey || round.NewThreshold != 2 {
		t.Fatalf("rekey round = %+v", round)
	}
	_, err = tr.TenantBarrierOfficerRevoke(ctx, &transport.TenantOfficerRevokeRequest{TenantID: tenantHolderTenant, Username: "bob"})
	var breaks *seal.RevocationBreaksQuorumError
	if !errors.As(err, &breaks) {
		t.Fatalf("revoking a holder of a 2-of-2 barrier = %v, want RevocationBreaksQuorumError", err)
	}
}

// TestTransport_TenantHolders_ServiceRefusals verifies the tenant service's
// refusals come back through each verb: one tenant's admin reaches none of
// another tenant's holder verbs, and a rekey of a barrier that was never
// initialized opens no round.
func TestTransport_TenantHolders_ServiceRefusals(t *testing.T) {
	tr, svc := newTenantHolderTransport(t)
	ctx := tenantHolderCtx()
	const other = "globex"
	if _, err := svc.CreateTenant(context.Background(), other, "Globex", ""); err != nil {
		t.Fatal(err)
	}
	calls := map[string]func() error{
		"OfficerRegister": func() error {
			_, err := tr.TenantBarrierOfficerRegister(ctx, &transport.TenantOfficerRegisterRequest{TenantID: other, Username: "alice"})
			return err
		},
		"OfficerList": func() error { _, err := tr.TenantBarrierOfficerList(ctx, other); return err },
		"QuorumBegin": func() error {
			_, err := tr.TenantBarrierQuorumBegin(ctx, &transport.TenantQuorumBeginRequest{TenantID: other, Op: transport.OpUnseal, Username: "alice"})
			return err
		},
		"QuorumSubmit": func() error {
			_, err := tr.TenantBarrierQuorumSubmit(ctx, &transport.TenantQuorumSubmitRequest{TenantID: other, Op: transport.OpUnseal, Username: "alice"})
			return err
		},
		"RekeyStart": func() error {
			_, err := tr.TenantBarrierRekeyStart(ctx, &transport.TenantRekeyStartRequest{TenantID: other, Threshold: 2})
			return err
		},
		"OfficerRevoke": func() error {
			_, err := tr.TenantBarrierOfficerRevoke(ctx, &transport.TenantOfficerRevokeRequest{TenantID: other, Username: "alice"})
			return err
		},
	}
	for name, call := range calls {
		var scope *tenant.TenantScopeError
		if err := call(); !errors.As(err, &scope) || scope.CallerTenantID != tenantHolderTenant || scope.TargetTenantID != other {
			t.Errorf("%s by %s's admin on %s = %v, want TenantScopeError", name, tenantHolderTenant, other, err)
		}
	}
	_, err := tr.TenantBarrierRekeyStart(ctx, &transport.TenantRekeyStartRequest{TenantID: tenantHolderTenant, Threshold: 2})
	var barrierErr *tenant.BarrierError
	if !errors.As(err, &barrierErr) || barrierErr.TenantID != tenantHolderTenant {
		t.Fatalf("a rekey of a barrier never initialized = %v, want BarrierError", err)
	}
}

// TestTransport_TenantHolders_Unconfigured verifies every tenant holder verb
// refuses, without dereferencing, on a transport with no tenant service and
// on a closed one.
func TestTransport_TenantHolders_Unconfigured(t *testing.T) {
	ctx := context.Background()
	calls := func(tr *Transport) map[string]func() error {
		return map[string]func() error{
			"OfficerRegister": func() error {
				_, err := tr.TenantBarrierOfficerRegister(ctx, &transport.TenantOfficerRegisterRequest{TenantID: "acme"})
				return err
			},
			"OfficerList": func() error { _, err := tr.TenantBarrierOfficerList(ctx, "acme"); return err },
			"QuorumBegin": func() error {
				_, err := tr.TenantBarrierQuorumBegin(ctx, &transport.TenantQuorumBeginRequest{TenantID: "acme"})
				return err
			},
			"QuorumSubmit": func() error {
				_, err := tr.TenantBarrierQuorumSubmit(ctx, &transport.TenantQuorumSubmitRequest{TenantID: "acme"})
				return err
			},
			"RekeyStart": func() error {
				_, err := tr.TenantBarrierRekeyStart(ctx, &transport.TenantRekeyStartRequest{TenantID: "acme"})
				return err
			},
			"OfficerRevoke": func() error {
				_, err := tr.TenantBarrierOfficerRevoke(ctx, &transport.TenantOfficerRevokeRequest{TenantID: "acme"})
				return err
			},
		}
	}
	for name, call := range calls(newExtTransport(t)) {
		var nilSvc *NilServiceError
		if err := call(); !errors.As(err, &nilSvc) || nilSvc.Service != "TenantServicer" {
			t.Errorf("%s with no tenant service = %v, want NilServiceError{TenantServicer}", name, err)
		}
	}
	for name, call := range calls(closedExtTransport(t, WithTenantService(&mockTenantService{}))) {
		var closed *transport.ClosedError
		if err := call(); !errors.As(err, &closed) {
			t.Errorf("%s on a closed transport = %v, want ClosedError", name, err)
		}
	}
}
