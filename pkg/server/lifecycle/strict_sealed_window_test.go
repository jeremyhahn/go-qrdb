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

package lifecycle_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony/ceremonytest"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// TestBuildSealServicesOnly_StrictRefusesCeremonyWithoutSO proves the
// sealed-window composition carries the strict separation posture to the
// initial ceremony: one that names no system SO is refused in preflight,
// leaving no PIN, barrier or admin credential behind. Without the posture
// the ceremony would commit initialization that first boot then refuses,
// and an initialized node cannot redo it.
func TestBuildSealServicesOnly_StrictRefusesCeremonyWithoutSO(t *testing.T) {
	ctx := context.Background()
	barrier, registry, credSvc := newTestBarrier(t)
	pins := pintest.NewBackend(t)
	ca, _ := ceremonytest.NewCA(t, "strict-test-ca")
	node, err := lifecycle.BuildSealServicesOnly(ctx, lifecycle.Infra{
		DataDir:            t.TempDir(),
		SealBarrier:        barrier,
		BarrierRegistry:    registry,
		CredentialCoreSvc:  credSvc,
		CeremonyCA:         ca,
		ClusterTrustDomain: "cluster.test",
		PINBackend:         pins,
		Logger:             slog.Default(),
		SeparationOfDuties: true,
	})
	if err != nil {
		t.Fatalf("BuildSealServicesOnly: %v", err)
	}
	defer func() { _ = node.Stop(ctx) }()

	core, ok := service.CeremonyServiceCore(node.Ceremony)
	if !ok {
		t.Fatal("CeremonyServiceCore: core service not exposed")
	}
	_, err = core.Initialize(ctx, ca, &ceremony.InitializeOptions{
		SOPin:       "test-so-pin",
		UserPin:     "test-user-pin",
		AdminCSRPEM: strictTestCSRPEM(t),
	})
	var rejected *ceremony.SystemSORejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("Initialize = %v, want *ceremony.SystemSORejectedError", err)
	}
	if pins.IsInitialized() {
		t.Error("the PINs must stay uncommitted")
	}
	// newTestBarrier's barrier is already unsealed for the credential
	// store; the ceremony's own state is what an accepted Initialize moves.
	if got := core.State(); got != ceremony.StateAwaitingInit {
		t.Errorf("ceremony state = %q, want %q: nothing may be committed", got, ceremony.StateAwaitingInit)
	}
	if last, _ := core.LastIssuedAdminCertPEM(); last != nil {
		t.Error("no admin credential may be issued")
	}
}

// strictTestCSRPEM returns a PEM CSR over a fresh P-256 key.
func strictTestCSRPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "admin"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
}
