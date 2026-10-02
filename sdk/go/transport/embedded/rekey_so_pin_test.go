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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony/ceremonytest"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// rekeySOPIN is the SO PIN the rekey fixtures provision.
const rekeySOPIN = "so-pin-1234"

// newRekeyTransport is the embedded transport over the REAL barrier service
// and ceremony: a 2-of-2 Shamir system barrier with two enrolled holders,
// initialized, and the PIN backend pins -- decorated with the node's SO PIN
// lockout -- shared by the ceremony and the transport, as a node wires them.
func newRekeyTransport(t *testing.T, pins pin.PINBackend) *Transport {
	t.Helper()
	pins = pin.NewLockoutBackend(pins)
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "admin"})
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
	ca, _ := ceremonytest.NewCA(t, "Rekey SO PIN CA")
	core.SetCA(ca)
	core.SetPINBackend(pins)
	svc := service.NewBarrierServiceWithCeremony(barrier, core)
	for _, name := range []string{"alice", "bob"} {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: name}}, key)
		if err != nil {
			t.Fatal(err)
		}
		csr := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
		if _, err := svc.BarrierOfficerRegister(ctx, seal.DomainUnseal, &service.BarrierOfficerRegisterRequest{Username: name, CSRPEM: csr}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := svc.BarrierInitializeShamir(ctx, 2, 2, ""); err != nil {
		t.Fatal(err)
	}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithPINBackend(pins), WithBarrierService(svc))
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// TestRekeySOPIN_RefusedUntilProvisioned: rekey start and holder revocation
// are SO-PIN-gated. A node on which no SO PIN has been provisioned has no PIN
// to check them against, so it refuses them rather than admitting any PIN.
func TestRekeySOPIN_RefusedUntilProvisioned(t *testing.T) {
	tr := newRekeyTransport(t, pintest.NewBackend(t))
	ctx := context.Background()
	if _, err := tr.BarrierRekeyStart(ctx, &transport.RekeyStartRequest{Domain: transport.DomainUnseal, Threshold: 2, SOPin: "anything-at-all"}); !errors.Is(err, pin.ErrPINNotSet) {
		t.Fatalf("rekey start with no SO PIN provisioned = %v, want pin.ErrPINNotSet", err)
	}
	if _, err := tr.BarrierOfficerRevoke(ctx, &transport.OfficerRevokeRequest{Domain: transport.DomainUnseal, Username: "bob", SOPin: "anything-at-all"}); !errors.Is(err, pin.ErrPINNotSet) {
		t.Fatalf("revoke with no SO PIN provisioned = %v, want pin.ErrPINNotSet", err)
	}
}

// TestRekeySOPIN_GuessesAreLockedOut: every rekey start or revocation is an
// online guess at the SO PIN, so they count against the node's one SO PIN
// lockout. After the free failures -- here spent on
// revocations -- even the right PIN is refused, for rekey too, until the
// backoff passes.
func TestRekeySOPIN_GuessesAreLockedOut(t *testing.T) {
	pins := pintest.NewBackend(t)
	if err := pins.SetSOPIN("", rekeySOPIN); err != nil {
		t.Fatal(err)
	}
	tr := newRekeyTransport(t, pins)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_, err := tr.BarrierOfficerRevoke(ctx, &transport.OfficerRevokeRequest{Domain: transport.DomainUnseal, Username: "bob", SOPin: "wrong-pin-0000"})
		if !errors.Is(err, pin.ErrPINInvalid) {
			t.Fatalf("wrong PIN %d = %v, want pin.ErrPINInvalid", i+1, err)
		}
	}
	_, err := tr.BarrierRekeyStart(ctx, &transport.RekeyStartRequest{Domain: transport.DomainUnseal, Threshold: 2, SOPin: rekeySOPIN})
	if !errors.Is(err, pin.ErrPINLocked) {
		t.Fatalf("the right PIN during the lockout = %v, want pin.ErrPINLocked", err)
	}
}

// TestRekeySOPIN_RightPINOpensTheRound is the positive control.
func TestRekeySOPIN_RightPINOpensTheRound(t *testing.T) {
	pins := pintest.NewBackend(t)
	if err := pins.SetSOPIN("", rekeySOPIN); err != nil {
		t.Fatal(err)
	}
	tr := newRekeyTransport(t, pins)
	round, err := tr.BarrierRekeyStart(context.Background(), &transport.RekeyStartRequest{Domain: transport.DomainUnseal, Threshold: 2, SOPin: rekeySOPIN})
	if err != nil {
		t.Fatalf("rekey start with the SO PIN: %v", err)
	}
	if round.Required != 2 {
		t.Fatalf("round = %+v", round)
	}
}

// TestRekeySOPIN_RecoveryGuessesLockRekey: a wrong SO PIN spent on another
// transport-checked operation -- recovery-key generation -- counts against
// the same lockout as a rekey, so the right PIN is then refused for the rekey.
func TestRekeySOPIN_RecoveryGuessesLockRekey(t *testing.T) {
	pins := pintest.NewBackend(t)
	if err := pins.SetSOPIN("", rekeySOPIN); err != nil {
		t.Fatal(err)
	}
	tr := newRekeyTransport(t, pins)
	ctx := context.Background()
	for i := 0; i < auth.SOPINFreeFailures; i++ {
		if _, err := tr.BarrierGenerateRecoveryKeys(ctx, &transport.BarrierGenerateRecoveryKeysRequest{Threshold: 2, SOPin: "wrong-pin-0000"}); !errors.Is(err, pin.ErrPINInvalid) {
			t.Fatalf("wrong PIN %d = %v, want pin.ErrPINInvalid", i+1, err)
		}
	}
	var locked *pin.SOPINLockedError
	if _, err := tr.BarrierRekeyStart(ctx, &transport.RekeyStartRequest{Domain: transport.DomainUnseal, Threshold: 2, SOPin: rekeySOPIN}); !errors.As(err, &locked) {
		t.Fatalf("the right PIN during the lockout = %v, want *pin.SOPINLockedError", err)
	}
}

// TestWithPINBackend_InstallsTheLockout: a backend handed to the embedded
// transport undecorated is decorated by WithPINBackend, so the transport's
// own SO PIN checks are held to the lockout.
func TestWithPINBackend_InstallsTheLockout(t *testing.T) {
	pins := pintest.NewBackend(t)
	if err := pins.SetSOPIN("", rekeySOPIN); err != nil {
		t.Fatal(err)
	}
	tr, err := New(newMockKVService(), &mockAdminService{}, WithPINBackend(pins), WithBarrierService(&mockBarrierService{}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for i := 0; i < auth.SOPINFreeFailures; i++ {
		if _, err := tr.BarrierGenerateRecoveryKeys(ctx, &transport.BarrierGenerateRecoveryKeysRequest{Threshold: 2, SOPin: "wrong-pin-0000"}); !errors.Is(err, pin.ErrPINInvalid) {
			t.Fatalf("wrong PIN %d = %v", i+1, err)
		}
	}
	if _, err := tr.BarrierGenerateRecoveryKeys(ctx, &transport.BarrierGenerateRecoveryKeysRequest{Threshold: 2, SOPin: rekeySOPIN}); !errors.Is(err, pin.ErrPINLocked) {
		t.Fatalf("the right PIN after the free failures = %v, want the lockout", err)
	}
}

// TestInitializeOnInitialized_RefusedBeforePIN: both initialize paths refuse
// an initialized barrier before comparing the SO PIN, so a right and a wrong
// PIN get the same answer and neither counts against the lockout.
func TestInitializeOnInitialized_RefusedBeforePIN(t *testing.T) {
	pins := pintest.NewBackend(t)
	if err := pins.SetSOPIN("", rekeySOPIN); err != nil {
		t.Fatal(err)
	}
	tr := newRekeyTransport(t, pins)
	ctx := context.Background()
	for _, presented := range []string{"wrong-pin-0000", rekeySOPIN} {
		for i := 0; i < auth.SOPINFreeFailures; i++ {
			if err := tr.BarrierInitialize(ctx, &transport.BarrierInitializeRequest{Secret: []byte("secret-secret-1234"), SOPin: presented}); !errors.Is(err, seal.ErrAlreadyInitialized) {
				t.Fatalf("initialize with SO PIN %q = %v, want seal.ErrAlreadyInitialized", presented, err)
			}
			if _, err := tr.BarrierInitializeShamir(ctx, &transport.BarrierInitializeShamirRequest{Threshold: 2, TotalShares: 2, SOPin: presented}); !errors.Is(err, seal.ErrAlreadyInitialized) {
				t.Fatalf("Shamir initialize with SO PIN %q = %v, want seal.ErrAlreadyInitialized", presented, err)
			}
		}
	}
	if _, err := tr.BarrierRekeyStart(ctx, &transport.RekeyStartRequest{Domain: transport.DomainUnseal, Threshold: 2, SOPin: rekeySOPIN}); err != nil {
		t.Fatalf("rekey with the right PIN after initialize calls = %v; they must not count against the lockout", err)
	}
}
