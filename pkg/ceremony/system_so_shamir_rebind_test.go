// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: AGPL-3.0-only

package ceremony_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"log/slog"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/factortest"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors/mtls"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestCeremonyServiceWithAdminRebind_ShamirSystemSOPersistRetry drives the
// SO-persist retry through the composition qrdbd and clustered SDK nodes
// serve: the core wrapped in service.CeremonyServiceWithAdminRebind. A
// 2-of-3 ceremony whose SO record write failed restarts enrolling with no SO
// record; the retried Initialize must reach the core, which persists the SO,
// rather than the wrapper's admin reissue.
func TestCeremonyServiceWithAdminRebind_ShamirSystemSOPersistRetry(t *testing.T) {
	ctx := context.Background()
	f := newShamirSOPersistFault(t)
	pending, err := f.service.PendingSystemSO(ctx)
	require.NoError(t, err)
	require.Nil(t, pending, "the failed write left no SO record")

	composed := service.NewCeremonyServiceWithAdminRebind(service.NewCeremonyService(f.service), f.service, nil, nil, nil)

	wrongPIN := serviceInitOptions(f.options())
	wrongPIN.SOPin = "not-the-so-pin"
	_, err = composed.CeremonyInitialize(ctx, f.ca, wrongPIN)
	require.Error(t, err, "the retry must prove the provisioned SO PIN")

	result, err := composed.CeremonyInitialize(ctx, f.ca, serviceInitOptions(f.options()))
	require.NoError(t, err, "the retried ceremony persists the SO")
	require.NotNil(t, result)
	assert.Equal(t, ceremony.StateEnrolling, f.service.State())

	pending, err = f.reopen().PendingSystemSO(ctx)
	require.NoError(t, err)
	require.NotNil(t, pending, "first boot is handed the SO")
	assert.Equal(t, "secofficer", pending.Username)
}

// serviceInitOptions carries the core options onto the service surface the
// composed ceremony serves.
func serviceInitOptions(o *ceremony.InitializeOptions) *service.CeremonyInitializeOptions {
	out := &service.CeremonyInitializeOptions{
		SOPin:            o.SOPin,
		UserPin:          o.UserPin,
		Threshold:        o.Threshold,
		Policy:           o.Policy,
		AdminCSRPEM:      string(o.AdminCSRPEM),
		AdminCertPEM:     string(o.AdminCertPEM),
		AdminCertPoolPEM: string(o.AdminCertPoolPEM),
		AdminGenerate:    o.AdminGenerate,
		SOUsername:       o.SOUsername,
		SOCSRPEM:         string(o.SOCSRPEM),
		SOCertPEM:        string(o.SOCertPEM),
		SOChainPEM:       string(o.SOChainPEM),
	}
	for _, officer := range o.Officers {
		out.Officers = append(out.Officers, service.CeremonyOfficerConfig{
			Username:      officer.Username,
			Role:          officer.Role,
			CSRPEM:        string(officer.CSRPEM),
			EncryptionKEM: officer.EncryptionKEM,
			EncryptionKey: officer.EncryptionKey,
			Factors:       officer.Factors,
			Designated:    officer.Designated,
		})
	}
	return out
}

// The fixture below builds the fault state from the package's exported API
// only, so this external test type-checks without any in-package test file.

// rebindPendingSystemSOKey is the storage key suffix of the system SO record.
const rebindPendingSystemSOKey = "ceremony/pending-system-so"

var errRebindSOWrite = errors.New("injected system SO record write failure")

// rebindSOWriteFailBackend fails the first write of the system SO record.
type rebindSOWriteFailBackend struct {
	seal.StorageBackend
	armed bool
}

func (b *rebindSOWriteFailBackend) Put(ctx context.Context, key string, value []byte) error {
	if b.armed && strings.HasSuffix(key, rebindPendingSystemSOKey) {
		b.armed = false
		return errRebindSOWrite
	}
	return b.StorageBackend.Put(ctx, key, value)
}

// rebindHolder is one share holder: its certificate key, CSR, enrolled
// thumbprint and claimed share envelope.
type rebindHolder struct {
	username string
	key      crypto.Signer
	csrPEM   []byte
	thumb    string
	envelope []byte
}

func newRebindHolder(t *testing.T, username string) *rebindHolder {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "admin", OrganizationalUnit: []string{"system", "security-officer"}},
	}, key)
	require.NoError(t, err)
	return &rebindHolder{
		username: username,
		key:      key,
		csrPEM:   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}),
	}
}

// shamirSOPersistFault is a 2-of-3 ceremony whose system SO record write
// failed, restarted and unsealed by two holders: service is enrolling, its
// barrier is unsealed and it holds no SO record, the state a retried
// Initialize must repair.
type shamirSOPersistFault struct {
	service *ceremony.CeremonyService
	ca      *ceremony.LocalCA
	// options returns the ceremony's original options, the ones a retry
	// repeats.
	options func() *ceremony.InitializeOptions
	// reopen builds a fresh service over the same storage.
	reopen func() *ceremony.CeremonyService
}

func newShamirSOPersistFault(t *testing.T) *shamirSOPersistFault {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	backend := &rebindSOWriteFailBackend{StorageBackend: seal.NewMemoryBackend(), armed: true}
	ca := rebindTestCA(t)
	pins := pintest.NewBackend(t)
	factors := authfactors.NewFactorRegistry()
	require.NoError(t, factors.Register(factortest.New("fido2", 0)))

	barrier := func() *seal.StorageBarrier {
		strategy, err := seal.NewShamirStrategy(2, 3)
		require.NoError(t, err)
		b, err := seal.NewStorageBarrier(logger, backend, seal.BarrierConfig{
			RootKeyPath: "test/root-key",
			Shamir:      &seal.ShamirConfig{Threshold: 2, TotalShares: 3},
		}, strategy)
		require.NoError(t, err)
		return b
	}
	newService := func(b *seal.StorageBarrier) *ceremony.CeremonyService {
		svc, err := ceremony.NewCeremonyService(&ceremony.CeremonyConfig{DataDir: t.TempDir()}, b, logger)
		require.NoError(t, err)
		svc.SetCA(ca)
		svc.SetTrustDomain("example.com")
		svc.SetFactorRegistry(factors)
		svc.SetPINBackend(pins)
		return svc
	}
	svc := newService(barrier())

	alice := newRebindHolder(t, "alice")
	bob := newRebindHolder(t, "bob")
	carol := newRebindHolder(t, "carol")
	soKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	soDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "ignored-cn"}}, soKey)
	require.NoError(t, err)
	soCSR := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: soDER})
	opts := func() *ceremony.InitializeOptions {
		o := &ceremony.InitializeOptions{
			SOPin: "so-pin-1234", UserPin: "user-pin-1234", Threshold: 2,
			Enroller: "admin", Hostname: "db.example.com", AdminGenerate: true,
			SOUsername: "secofficer", SOCSRPEM: soCSR,
		}
		for _, h := range []*rebindHolder{alice, bob, carol} {
			o.Officers = append(o.Officers, ceremony.OfficerConfig{Username: h.username, Role: seal.RoleSecurityOfficer, CSRPEM: h.csrPEM})
		}
		return o
	}

	_, err = svc.Initialize(ctx, ca, opts())
	require.ErrorIs(t, err, errRebindSOWrite)
	require.False(t, backend.armed, "the fault hit the SO record write")

	officers, err := svc.ListOfficers(ctx, seal.DomainUnseal)
	require.NoError(t, err)
	for _, o := range officers {
		for _, h := range []*rebindHolder{alice, bob} {
			if o.Username == h.username {
				h.thumb = o.CertThumbprint
			}
		}
	}
	for _, h := range []*rebindHolder{alice, bob} {
		ch, err := svc.BeginClaimShare(ctx, seal.DomainUnseal, h.username, h.thumb)
		require.NoError(t, err, h.username)
		sig, err := ceremony.SignShareClaim(h.key, ceremony.ScopeSystem.ClaimLabel(), h.username, ch.Nonce)
		require.NoError(t, err)
		res, err := svc.CompleteClaimShare(ctx, seal.DomainUnseal, h.username, hex.EncodeToString(ch.Nonce), sig, h.thumb)
		require.NoError(t, err, h.username)
		h.envelope = res.Envelope
	}

	b2 := barrier()
	require.True(t, b2.IsSealed())
	svc2 := newService(b2)
	require.Equal(t, ceremony.StateEnrolling, svc2.State())
	for _, h := range []*rebindHolder{alice, bob} {
		ch, err := svc2.BeginQuorum(ctx, seal.DomainUnseal, seal.OpUnseal, h.username, "")
		require.NoError(t, err)
		resp, err := mtls.SignChallenge(h.key, ch.ChallengeID, ch.MethodData[mtls.MethodDataPurpose], ch.Nonce)
		require.NoError(t, err)
		env, err := ceremony.ParseShareEnvelope(h.envelope)
		require.NoError(t, err)
		share, err := env.OpenWithKey(h.key)
		require.NoError(t, err)
		_, err = b2.SubmitHolderShare(ctx, &seal.HolderSubmission{
			Domain: seal.DomainUnseal,
			Op:     seal.OpUnseal,
			Holder: h.username,
			Proof:  seal.HolderProof{ChallengeID: ch.ChallengeID, Factor: ch.Factor, Response: resp},
			Share:  share,
		})
		require.NoError(t, err)
	}
	require.False(t, b2.IsSealed())

	return &shamirSOPersistFault{
		service: svc2,
		ca:      ca,
		options: opts,
		reopen:  func() *ceremony.CeremonyService { return newService(b2) },
	}
}

// rebindTestCA is a self-signed local ceremony CA.
func rebindTestCA(t *testing.T) *ceremony.LocalCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Test Ceremony CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	ca, err := ceremony.NewLocalCA(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	)
	require.NoError(t, err)
	return ca
}
