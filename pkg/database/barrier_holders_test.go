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

package database

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

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony/ceremonytest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// dbHolder is one database share holder's client side.
type dbHolder struct {
	name     string
	key      *ecdsa.PrivateKey
	csr      []byte
	thumb    string
	envelope []byte
}

func newDBHolder(t *testing.T, name string) *dbHolder {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "ignored"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return &dbHolder{name: name, key: key, csr: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})}
}

// holderTrustDomain is the SPIFFE trust domain holder certificates are issued
// under. authtest.ClientTrustDomain cannot be imported here: authtest imports
// this package.
const holderTrustDomain = "example.com"

// holderBarrierManager is a barrier manager with share-holder custody wired.
func holderBarrierManager(t *testing.T, ca *ceremony.LocalCA) *DatabaseBarrierManager {
	t.Helper()
	bm := newTestBarrierManager(t)
	bm.SetHolderDeps(ceremony.HolderDeps{CA: ca, TrustDomain: holderTrustDomain})
	return bm
}

// shamirDBConfig is a 2-of-2 Shamir database on a dedicated store.
func shamirDBConfig(t *testing.T, name string) DatabaseConfig {
	t.Helper()
	cfg := validDedicatedMemoryConfig(t, name)
	cfg.TenantID = "acme"
	cfg.Barrier = &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModeShamir, SharesRequired: 2, SharesTotal: 2}
	return cfg
}

// enrolAndClaim enrols holders on dbName, initializes custody and has each
// holder claim its envelope.
func enrolAndClaim(t *testing.T, bm *DatabaseBarrierManager, dbName string, holders ...*dbHolder) {
	t.Helper()
	ctx := context.Background()
	svc, _, err := bm.Holders(dbName, "acme")
	if err != nil {
		t.Fatalf("Holders: %v", err)
	}
	for _, h := range holders {
		res, err := svc.RegisterOfficer(ctx, ceremony.RegisterOfficerRequest{Username: h.name, Role: seal.RoleCustodian, CSRPEM: h.csr, Enroller: "db-admin"})
		if err != nil {
			t.Fatalf("RegisterOfficer(%s): %v", h.name, err)
		}
		cert, err := mtls.ParseCertificatePEM(res.CertPEM)
		if err != nil {
			t.Fatal(err)
		}
		h.thumb = mtls.Thumbprint(cert.Raw)
	}
	if _, err := bm.InitializeShamir(ctx, dbName, "acme", ""); err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}
	for _, h := range holders {
		ch, err := svc.BeginClaimShare(ctx, h.name, h.thumb)
		if err != nil {
			t.Fatal(err)
		}
		sig, err := ceremony.SignShareClaim(h.key, ceremony.DatabaseClaimLabel(dbName), h.name, ch.Nonce)
		if err != nil {
			t.Fatal(err)
		}
		res, err := svc.CompleteClaimShare(ctx, h.name, hex.EncodeToString(ch.Nonce), sig, h.thumb)
		if err != nil {
			t.Fatal(err)
		}
		h.envelope = res.Envelope
	}
}

// submitDB answers the mtls factor and submits h's share to op on dbName.
func submitDB(t *testing.T, bm *DatabaseBarrierManager, dbName string, op seal.QuorumOp, h *dbHolder) (*seal.QuorumOutcome, error) {
	t.Helper()
	ctx := context.Background()
	svc, barrier, err := bm.Holders(dbName, "acme")
	if err != nil {
		return nil, err
	}
	ch, err := svc.BeginQuorum(ctx, barrier, op, h.name, "")
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
	return bm.SubmitHolderShare(ctx, dbName, "acme", &seal.HolderSubmission{
		Domain: seal.DomainUnseal, Op: op, Holder: h.name, Share: share,
		Proof: seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: sig},
	}, nil)
}

// TestShamirDatabase_Lifecycle drives a database barrier under Shamir
// custody: a create registers it uninitialized and its data fails closed;
// holders enrol and custody initializes; single-caller seal and credential
// unseal are refused; a quorum seals and unseals it; it reopens sealed under
// its holders' authority; and dropping it removes the scope.
func TestShamirDatabase_Lifecycle(t *testing.T) {
	ctx := context.Background()
	ca, _ := ceremonytest.NewCA(t, "Database Holder CA")
	dataDir := t.TempDir()
	bm := holderBarrierManager(t, ca)
	mgr := newCatalogManager(nil, dataDir, WithBarrierManager(bm))

	cfg := shamirDBConfig(t, "vault")
	db, err := mgr.CreateWithCredentials(ctx, cfg, seal.Credentials{})
	if err != nil {
		t.Fatalf("CreateWithCredentials: %v", err)
	}
	if err := db.Backend().Put(ctx, []byte("k"), []byte("v")); err == nil {
		t.Fatal("a write reached an uninitialized shamir database")
	}

	alice, bob := newDBHolder(t, "alice"), newDBHolder(t, "bob")
	enrolAndClaim(t, bm, "vault", alice, bob)
	if err := db.Backend().Put(ctx, []byte("k"), []byte("v")); err != nil {
		t.Fatalf("write after initialization: %v", err)
	}

	if err := bm.SealDatabase("vault", "acme"); !errors.Is(err, seal.ErrShamirBarrierRequiresQuorum) {
		t.Fatalf("single-caller seal = %v, want ErrShamirBarrierRequiresQuorum", err)
	}
	if _, err := submitDB(t, bm, "vault", seal.OpSeal, alice); err != nil {
		t.Fatal(err)
	}
	out, err := submitDB(t, bm, "vault", seal.OpSeal, bob)
	if err != nil || !out.Complete {
		t.Fatalf("seal quorum = %+v, %v", out, err)
	}
	if _, err := db.Backend().Get(ctx, []byte("k")); err == nil {
		t.Fatal("a read reached a sealed database")
	}
	if err := bm.UnsealDatabase(ctx, "vault", "acme", testCreds()); !errors.Is(err, seal.ErrShamirUnsealRequiresQuorum) {
		t.Fatalf("credential unseal = %v, want ErrShamirUnsealRequiresQuorum", err)
	}

	// Reopen: the barrier comes back sealed under its holders' authority.
	if err := mgr.Close(); err != nil {
		t.Fatal(err)
	}
	bm2 := holderBarrierManager(t, ca)
	mgr2 := newCatalogManager(nil, dataDir, WithBarrierManager(bm2))
	defer func() { _ = mgr2.Close() }()
	db2, err := mgr2.Open(context.Background(), "acme", "vault")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if db2.Status() != StatusSealed {
		t.Fatalf("reopened status = %q, want sealed", db2.Status())
	}
	if _, err := submitDB(t, bm2, "vault", seal.OpUnseal, bob); err != nil {
		t.Fatal(err)
	}
	out, err = submitDB(t, bm2, "vault", seal.OpUnseal, alice)
	if err != nil || !out.Complete {
		t.Fatalf("unseal quorum = %+v, %v", out, err)
	}

	if err := mgr2.Drop(ctx, "vault"); err != nil {
		t.Fatalf("Drop: %v", err)
	}
	if _, _, err := bm2.Holders("vault", "acme"); err == nil {
		t.Fatal("a dropped database still has a holder authority")
	}
}

// TestShamirDatabase_Refusals covers what a Shamir database barrier refuses:
// a secret at create, no holder custody configured, an invalid M-of-N, and a
// share-holder operation on a database with no Shamir barrier.
func TestShamirDatabase_Refusals(t *testing.T) {
	ctx := context.Background()
	ca, _ := ceremonytest.NewCA(t, "Database Holder CA")

	bm := holderBarrierManager(t, ca)
	cfg := shamirDBConfig(t, "with-secret")
	err := bm.EnsureBarrier(ctx, cfg.Name, cfg.TenantID, cfg.Barrier, seal.NewMemoryBackend(), testCreds())
	var secretErr *ShamirSecretNotAllowedError
	if !errors.As(err, &secretErr) {
		t.Fatalf("create with a secret = %v, want ShamirSecretNotAllowedError", err)
	}

	noCustody := newTestBarrierManager(t)
	err = noCustody.EnsureBarrier(ctx, cfg.Name, cfg.TenantID, cfg.Barrier, seal.NewMemoryBackend(), seal.Credentials{})
	var custodyErr *ShamirCustodyNotConfiguredError
	if !errors.As(err, &custodyErr) || !errors.Is(err, seal.ErrCeremonyDistributorNotConfigured) {
		t.Fatalf("create without custody = %v, want ShamirCustodyNotConfiguredError", err)
	}

	bad := &DatabaseBarrierConfig{Enabled: true, Mode: BarrierModeShamir, SharesRequired: 3, SharesTotal: 2}
	if err := bm.EnsureBarrier(ctx, "bad-mofn", "acme", bad, seal.NewMemoryBackend(), seal.Credentials{}); !errors.Is(err, seal.ErrShamirThresholdInvalid) {
		t.Fatalf("create with 3-of-2 = %v, want ErrShamirThresholdInvalid", err)
	}

	var notShamir *NotShamirBarrierError
	if _, _, err := bm.Holders("absent", "acme"); !errors.As(err, &notShamir) {
		t.Fatalf("Holders(absent) = %v, want NotShamirBarrierError", err)
	}
	if _, err := bm.InitializeShamir(ctx, "absent", "acme", ""); !errors.As(err, &notShamir) {
		t.Fatalf("InitializeShamir(absent) = %v, want NotShamirBarrierError", err)
	}
	for _, msg := range []string{secretErr.Error(), custodyErr.Error(), notShamir.Error()} {
		if msg == "" {
			t.Fatal("empty error message")
		}
	}
}

// TestShamirDatabase_InitializeNeedsEveryHolder pins that initialization
// refuses a holder count different from the share count, as the system and
// tenant barriers do.
func TestShamirDatabase_InitializeNeedsEveryHolder(t *testing.T) {
	ctx := context.Background()
	ca, _ := ceremonytest.NewCA(t, "Database Holder CA")
	bm := holderBarrierManager(t, ca)
	mgr := newCatalogManager(nil, t.TempDir(), WithBarrierManager(bm))
	defer func() { _ = mgr.Close() }()
	cfg := shamirDBConfig(t, "short")
	cfg.Barrier.SharesTotal = 3
	if _, err := mgr.CreateWithCredentials(ctx, cfg, seal.Credentials{}); err != nil {
		t.Fatal(err)
	}
	svc, _, err := bm.Holders("short", "acme")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []*dbHolder{newDBHolder(t, "alice"), newDBHolder(t, "bob")} {
		if _, err := svc.RegisterOfficer(ctx, ceremony.RegisterOfficerRequest{Username: h.name, CSRPEM: h.csr, Enroller: "db-admin"}); err != nil {
			t.Fatal(err)
		}
	}
	var mismatch *seal.HolderCountMismatchError
	if _, err := bm.InitializeShamir(ctx, "short", "acme", ""); !errors.As(err, &mismatch) {
		t.Fatalf("InitializeShamir with 2 of 3 holders = %v, want HolderCountMismatchError", err)
	}
}

// TestShamirDatabase_CreatedSealed verifies a Shamir database reports itself
// sealed from its create until its custody is initialized: its barrier holds
// no key yet, so nothing on it is usable, and an "open" status would say
// otherwise.
func TestShamirDatabase_CreatedSealed(t *testing.T) {
	ctx := context.Background()
	ca, _ := ceremonytest.NewCA(t, "Database Holder CA")
	mgr := newCatalogManager(nil, t.TempDir(), WithBarrierManager(holderBarrierManager(t, ca)))

	db, err := mgr.CreateWithCredentials(ctx, shamirDBConfig(t, "vault"), seal.Credentials{})
	if err != nil {
		t.Fatalf("CreateWithCredentials: %v", err)
	}
	if got := db.Status(); got != StatusSealed {
		t.Fatalf("a created shamir database reports %q, want %q", got, StatusSealed)
	}
}

// TestDatabaseBarrierManager_SealAuthorizedSealsAQuorumBarrier: a Shamir
// database barrier requires a quorum for a single caller's seal, but the
// authorized seal -- a completed quorum's, or a cluster seal record's
// enforcement on every node holding the barrier -- seals it.
func TestDatabaseBarrierManager_SealAuthorizedSealsAQuorumBarrier(t *testing.T) {
	ctx := context.Background()
	ca, _ := ceremonytest.NewCA(t, "Database Holder CA")
	bm := holderBarrierManager(t, ca)
	mgr := newCatalogManager(nil, t.TempDir(), WithBarrierManager(bm))
	t.Cleanup(func() { _ = mgr.Close() })
	db, err := mgr.CreateWithCredentials(ctx, shamirDBConfig(t, "vault"), seal.Credentials{})
	if err != nil {
		t.Fatalf("CreateWithCredentials: %v", err)
	}
	enrolAndClaim(t, bm, "vault", newDBHolder(t, "alice"), newDBHolder(t, "bob"))

	if !bm.RequiresQuorum("vault", "acme") {
		t.Fatal("a shamir database barrier does not require a quorum")
	}
	if err := bm.SealAuthorized("vault", "acme"); err != nil {
		t.Fatalf("SealAuthorized: %v", err)
	}
	if _, err := db.Backend().Get(ctx, []byte("k")); err == nil {
		t.Fatal("a read reached a database whose barrier was sealed")
	}
}

// TestDatabaseBarrierManager_RequiresQuorumOnlyForShamir: a database barrier
// with no holders is sealed by one caller.
func TestDatabaseBarrierManager_RequiresQuorumOnlyForShamir(t *testing.T) {
	ca, _ := ceremonytest.NewCA(t, "Database Holder CA")
	bm := holderBarrierManager(t, ca)
	if bm.RequiresQuorum("absent", "acme") {
		t.Fatal("a database with no holders requires a quorum")
	}
	if err := bm.SealAuthorized("absent", "acme"); err == nil {
		t.Fatal("sealing a database with no barrier succeeded")
	}
}
