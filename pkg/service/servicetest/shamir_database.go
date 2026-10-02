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

// Package servicetest provides fixtures over real services for the tests of
// the transports that front them. A transport test drives the fixture's
// service through its own handlers or client, so what it proves is the
// transport's mapping of a real service's behaviour, not a double's.
package servicetest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony/ceremonytest"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ShamirDatabase is a database service holding one database whose barrier
// is under Shamir custody, created at M-of-N and not yet initialized: its
// holders are still to be enrolled.
type ShamirDatabase struct {
	// Service is the real database service.
	Service service.DatabaseServicer
	// Holders is Service's share-holder custody.
	Holders service.DatabaseHolderServicer
	// Name is the Shamir database's name.
	Name string
}

// NewShamirDatabase creates a database service over a system barrier and a
// CA, holding database name on a dedicated store with an m-of-n Shamir
// barrier. Everything lives in memory or under t's temporary directories.
func NewShamirDatabase(t testing.TB, name string, m, n int) *ShamirDatabase {
	t.Helper()
	return NewShamirDatabaseOwnedBy(t, "", name, m, n)
}

// NewShamirDatabaseOwnedBy is NewShamirDatabase with the database owned by
// tenantID ("" is the system tenant).
func NewShamirDatabaseOwnedBy(t testing.TB, tenantID, name string, m, n int) *ShamirDatabase {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := database.NewManager(logger, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	system, err := seal.NewStorageBarrier(logger, seal.NewMemoryBackend(), seal.BarrierConfig{RootKeyPath: "root-key"}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatal(err)
	}
	if err := system.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatal(err)
	}
	bm, err := database.NewDatabaseBarrierManager(logger, system)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := ceremonytest.NewCA(t, "Database Holder CA")
	bm.SetHolderDeps(ceremony.HolderDeps{CA: ca, TrustDomain: "db.example.com"})
	mgr.SetBarrierManager(bm)
	cfg := database.DatabaseConfig{
		TenantID: tenantID, Name: name, Engine: database.EngineKV, Backend: database.BackendMemory, DataDir: t.TempDir(),
		Barrier: &database.DatabaseBarrierConfig{Enabled: true, Mode: database.BarrierModeShamir, SharesRequired: m, SharesTotal: n},
	}
	if _, err := mgr.CreateWithCredentials(context.Background(), cfg, seal.Credentials{}); err != nil {
		t.Fatal(err)
	}
	svc := service.NewDatabaseService(mgr, service.WithBarrierManager(bm))
	holders, ok := svc.(service.DatabaseHolderServicer)
	if !ok {
		t.Fatal("servicetest: the database service has no share-holder custody")
	}
	return &ShamirDatabase{Service: svc, Holders: holders, Name: name}
}

// Claim runs h's claim handshake against the service directly, presenting
// h's certificate, and keeps the envelope it delivers. It is for a test
// whose transport cannot present a client certificate.
func (d *ShamirDatabase) Claim(t testing.TB, h *Holder) {
	t.Helper()
	ctx := context.Background()
	challenge, err := d.Holders.DatabaseBarrierShareClaimBegin(ctx, d.Name, h.Name, h.Thumbprint())
	if err != nil {
		t.Fatal(err)
	}
	result, err := d.Holders.DatabaseBarrierShareClaimComplete(ctx, d.Name, h.Name, challenge.NonceHex, h.SignClaimHex(t, d.Name, challenge.NonceHex), h.Thumbprint())
	if err != nil {
		t.Fatal(err)
	}
	h.Envelope = result.Envelope
}

// Holder is one share holder's client side: its key, the CSR it enrols
// with, the certificate the CA issued, and the envelope its claim delivered.
type Holder struct {
	Name     string
	Key      *ecdsa.PrivateKey
	CSRPEM   string
	Cert     *x509.Certificate
	Envelope []byte
}

// NewHolder generates a holder's key and CSR.
func NewHolder(t testing.TB, name string) *Holder {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "ignored"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return &Holder{Name: name, Key: key, CSRPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))}
}

// Enrolled records the certificate the enrolment issued.
func (h *Holder) Enrolled(t testing.TB, certPEM string) {
	t.Helper()
	cert, err := mtls.ParseCertificatePEM([]byte(certPEM))
	if err != nil {
		t.Fatal(err)
	}
	h.Cert = cert
}

// Thumbprint is the thumbprint of h's issued certificate.
func (h *Holder) Thumbprint() string {
	return mtls.Thumbprint(h.Cert.Raw)
}

// Sign answers the mtls challenge challengeID, bound to purpose (the
// challenge's mtls.MethodDataPurpose method data) and carrying nonce, with
// h's key, as the mtls factor requires (mtls.SignChallenge).
func (h *Holder) Sign(t testing.TB, challengeID, purpose string, nonce []byte) []byte {
	t.Helper()
	sig, err := mtls.SignChallenge(h.Key, challengeID, purpose, nonce)
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

// SignClaimHex signs h's claim of an envelope of database's barrier over the
// hex-encoded nonce a claim-begin returned (ceremony.SignShareClaim).
func (h *Holder) SignClaimHex(t testing.TB, database, nonceHex string) []byte {
	t.Helper()
	nonce, err := hex.DecodeString(nonceHex)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := ceremony.SignShareClaim(h.Key, ceremony.DatabaseClaimLabel(database), h.Name, nonce)
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

// Share opens h's envelope with its key. The caller owns the share.
func (h *Holder) Share(t testing.TB) []byte {
	t.Helper()
	env, err := ceremony.ParseShareEnvelope(h.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	share, err := env.OpenWithKey(h.Key)
	if err != nil {
		t.Fatal(err)
	}
	return share
}
