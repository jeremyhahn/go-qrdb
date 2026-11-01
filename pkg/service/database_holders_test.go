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

package service

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

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony/ceremonytest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// dbSvcHolder is one database share holder's client side.
type dbSvcHolder struct {
	name     string
	key      *ecdsa.PrivateKey
	csr      []byte
	thumb    string
	envelope []byte
}

func newDBSvcHolder(t *testing.T, name string) *dbSvcHolder {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "ignored"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return &dbSvcHolder{name: name, key: key, csr: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})}
}

// dbAdminCtx is a context authenticated as a database admin of tenantID.
func dbAdminCtx(tenantID string) context.Context {
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "db-admin", TenantID: tenantID})
	return tenant.WithTenantID(ctx, tenantID)
}

// newHolderDatabaseService is a database service whose barrier manager has
// share-holder custody wired, with a 2-of-2 Shamir database "vault" created
// in tenant acme on a dedicated store.
func newHolderDatabaseService(t *testing.T) (*DatabaseService, *database.Manager) {
	t.Helper()
	return newAuditedHolderDatabaseService(t, nil)
}

// newAuditedHolderDatabaseService is newHolderDatabaseService with trail as
// the holder authority's compliance audit trail.
func newAuditedHolderDatabaseService(t *testing.T, trail audit.AuditLogger) (*DatabaseService, *database.Manager) {
	t.Helper()
	svc, mgr := newTestDatabaseServiceWithBarrier(t)
	ca, _ := ceremonytest.NewCA(t, "Database Holder CA")
	svc.barrierMgr.SetHolderDeps(ceremony.HolderDeps{CA: ca, TrustDomain: ceremonyTrustDomain, Audit: trail})
	// Created through the manager: a tenant-scoped create discards a
	// caller-supplied data directory and only a volume or raft tenant gets a
	// dedicated store, which this test does not need to provision.
	cfg := database.DatabaseConfig{
		TenantID: "acme", Name: "vault", Backend: database.BackendMemory,
		DataDir: t.TempDir(),
		Barrier: &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModeShamir, SharesRequired: 2, SharesTotal: 2},
	}
	if _, err := mgr.CreateWithCredentials(context.Background(), cfg, seal.Credentials{}); err != nil {
		t.Fatalf("create: %v", err)
	}
	return svc, mgr
}

// submitDBSvc answers the mtls factor and submits h's share to op.
func submitDBSvc(t *testing.T, svc *DatabaseService, ctx context.Context, op seal.QuorumOp, h *dbSvcHolder) (*QuorumOutcomeInfo, error) {
	t.Helper()
	ch, err := svc.DatabaseBarrierQuorumBegin(ctx, "vault", &QuorumBeginRequest{Op: op, Username: h.name})
	if err != nil {
		return nil, err
	}
	sig, err := mtls.SignChallenge(h.key, ch.ChallengeID, ch.MethodData[mtls.MethodDataPurpose], ch.Nonce)
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
	return svc.DatabaseBarrierQuorumSubmit(ctx, "vault", &QuorumSubmitRequest{
		Op: op, Username: h.name, ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: sig, Share: share,
	})
}

// claimedDatabaseHolders enrols alice and bob on "vault", initializes it
// 2-of-2 and has both claim their envelopes.
func claimedDatabaseHolders(t *testing.T, svc *DatabaseService, ctx context.Context) []*dbSvcHolder {
	t.Helper()
	holders := []*dbSvcHolder{newDBSvcHolder(t, "alice"), newDBSvcHolder(t, "bob")}
	for _, h := range holders {
		res, err := svc.DatabaseBarrierOfficerRegister(ctx, "vault", &BarrierOfficerRegisterRequest{Username: h.name, Role: seal.RoleCustodian, CSRPEM: h.csr})
		if err != nil {
			t.Fatalf("register %s: %v", h.name, err)
		}
		cert, err := mtls.ParseCertificatePEM(res.CertPEM)
		if err != nil {
			t.Fatal(err)
		}
		h.thumb = mtls.Thumbprint(cert.Raw)
	}
	list, err := svc.DatabaseBarrierOfficerList(ctx, "vault")
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %d, %v", len(list), err)
	}

	info, err := svc.DatabaseBarrierShamirInitialize(ctx, "vault", "")
	if err != nil || info.Threshold != 2 || info.TotalShares != 2 || info.ShareCount != 2 {
		t.Fatalf("initialize = %+v, %v", info, err)
	}
	for _, h := range holders {
		ch, err := svc.DatabaseBarrierShareClaimBegin(ctx, "vault", h.name, h.thumb)
		if err != nil {
			t.Fatal(err)
		}
		nonce, err := hex.DecodeString(ch.NonceHex)
		if err != nil {
			t.Fatal(err)
		}
		sig, err := ceremony.SignShareClaim(h.key, ceremony.DatabaseClaimLabel("vault"), h.name, nonce)
		if err != nil {
			t.Fatal(err)
		}
		res, err := svc.DatabaseBarrierShareClaimComplete(ctx, "vault", h.name, ch.NonceHex, sig, h.thumb)
		if err != nil {
			t.Fatal(err)
		}
		h.envelope = res.Envelope
	}
	return holders
}

// TestDatabaseHolders_Lifecycle drives a Shamir database's holders through
// the database service: enrolment, initialization, claims, a quorum seal and
// unseal reflected on the database's status, and a rekey whose scheme is
// recorded.
func TestDatabaseHolders_Lifecycle(t *testing.T) {
	svc, mgr := newHolderDatabaseService(t)
	ctx := dbAdminCtx("acme")

	holders := claimedDatabaseHolders(t, svc, ctx)

	if err := svc.SealDatabase(ctx, "vault"); !errors.Is(err, seal.ErrShamirBarrierRequiresQuorum) {
		t.Fatalf("single-caller seal = %v, want ErrShamirBarrierRequiresQuorum", err)
	}
	for _, h := range holders {
		if _, err := submitDBSvc(t, svc, ctx, seal.OpSeal, h); err != nil {
			t.Fatal(err)
		}
	}
	if db, _ := mgr.Get("vault"); db.Status() != database.StatusSealed {
		t.Fatalf("status after a seal quorum = %q, want sealed", db.Status())
	}
	for _, h := range holders {
		if _, err := submitDBSvc(t, svc, ctx, seal.OpUnseal, h); err != nil {
			t.Fatal(err)
		}
	}
	if db, _ := mgr.Get("vault"); db.Status() != database.StatusOpen {
		t.Fatalf("status after an unseal quorum = %q, want open", db.Status())
	}

	round, err := svc.DatabaseBarrierRekeyStart(ctx, "vault", 2, "")
	if err != nil || round == nil {
		t.Fatalf("rekey start = %+v, %v", round, err)
	}
	var out *QuorumOutcomeInfo
	for _, h := range holders {
		if out, err = submitDBSvc(t, svc, ctx, seal.OpRekey, h); err != nil {
			t.Fatal(err)
		}
	}
	if !out.Complete || out.Rekey == nil {
		t.Fatalf("rekey outcome = %+v", out)
	}
	if db, _ := mgr.Get("vault"); db.Config().Barrier.SharesRequired != 2 || db.Config().Barrier.SharesTotal != 2 {
		t.Fatalf("recorded scheme = %+v", db.Config().Barrier)
	}

	var breaks *seal.RevocationBreaksQuorumError
	if _, err := svc.DatabaseBarrierOfficerRevoke(ctx, "vault", "bob", 2); !errors.As(err, &breaks) {
		t.Fatalf("revoking below the threshold = %v, want RevocationBreaksQuorumError", err)
	}
}

// TestDatabaseHolders_Refusals pins the checks every holder operation
// shares: another tenant is refused before it learns anything, a database
// with no Shamir barrier has no holders, and a missing request is refused.
func TestDatabaseHolders_Refusals(t *testing.T) {
	svc, _ := newHolderDatabaseService(t)
	other := dbAdminCtx("globex")
	ctx := dbAdminCtx("acme")

	// Another tenant's database is indistinguishable from a missing one.
	var denied *database.NotFoundError
	if _, err := svc.DatabaseBarrierOfficerList(other, "vault"); !errors.As(err, &denied) {
		t.Fatalf("another tenant's list = %v, want NotFoundError", err)
	}
	if _, err := svc.DatabaseBarrierShamirInitialize(other, "vault", ""); !errors.As(err, &denied) {
		t.Fatalf("another tenant's initialize = %v, want NotFoundError", err)
	}
	if _, err := svc.DatabaseBarrierQuorumSubmit(other, "vault", &QuorumSubmitRequest{Op: seal.OpUnseal, Username: "alice", Share: []byte("s")}); !errors.As(err, &denied) {
		t.Fatalf("another tenant's submission = %v, want NotFoundError", err)
	}

	if _, err := svc.DatabaseBarrierOfficerRegister(ctx, "vault", nil); !errors.Is(err, ErrNilRequest) {
		t.Fatalf("nil registration = %v, want ErrNilRequest", err)
	}
	if _, err := svc.DatabaseBarrierQuorumBegin(ctx, "vault", nil); !errors.Is(err, ErrNilRequest) {
		t.Fatalf("nil quorum begin = %v, want ErrNilRequest", err)
	}
	if _, err := svc.DatabaseBarrierQuorumSubmit(ctx, "vault", nil); !errors.Is(err, ErrNilRequest) {
		t.Fatalf("nil submission = %v, want ErrNilRequest", err)
	}
	if _, err := svc.DatabaseBarrierOfficerRevoke(ctx, "vault", "", 2); !errors.Is(err, ceremony.ErrMissingUsername) {
		t.Fatalf("revoke without a username = %v, want ErrMissingUsername", err)
	}

	plain := database.DatabaseConfig{Name: "plain", Backend: database.BackendMemory}
	if _, err := svc.CreateForTenant(ctx, plain); err != nil {
		t.Fatal(err)
	}
	var notShamir *database.NotShamirBarrierError
	if _, err := svc.DatabaseBarrierOfficerList(ctx, "plain"); !errors.As(err, &notShamir) {
		t.Fatalf("holders of a database with no Shamir barrier = %v, want NotShamirBarrierError", err)
	}

	noBarrier, ok := NewDatabaseService(databasetest.NewManager(nil, t.TempDir())).(*DatabaseService)
	if !ok {
		t.Fatal("not a *DatabaseService")
	}
	if _, err := noBarrier.Create(testDBConfig("x")); err != nil {
		t.Fatal(err)
	}
	var notConfigured *BarrierManagerNotConfiguredError
	if _, err := noBarrier.DatabaseBarrierOfficerList(context.Background(), "x"); !errors.As(err, &notConfigured) {
		t.Fatalf("a node with no barrier manager = %v", err)
	}
}
