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

package servicetest

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony/ceremonytest"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// RealAdminLookup is a minimal firstboot.AdminLookup/CertBinder double over
// a single in-memory admin record -- a STORE-SHAPED test double (go-qrdb#560
// review B1, re-review: "no mocks that return ErrPINInvalid themselves"). It
// never answers the SO-PIN question itself; that question is answered by
// the REAL pin.PINBackend wired into the REAL ceremony.CeremonyService
// NewRealReissueStack builds, through the REAL
// service.CeremonyServiceWithAdminRebind.
type RealAdminLookup struct {
	Admin *user.User
}

// GetUserByUsername implements firstboot.AdminLookup over Admin.
func (a *RealAdminLookup) GetUserByUsername(_ context.Context, tenantID, username string) (*user.User, error) {
	if tenantID != tenantid.System || username != user.BootstrapAdminUsername {
		return nil, &user.UserNotFoundError{Lookup: username}
	}
	return a.Admin, nil
}

// BindAdminCert implements firstboot.CertBinder over Admin, appending the
// binding in place -- callers that need to assert on Admin.CertBindings
// after a reissue read it straight off the struct this returned.
func (a *RealAdminLookup) BindAdminCert(_ context.Context, _ string, _ uint64, thumbprint string) error {
	a.Admin.CertBindings = append(a.Admin.CertBindings, user.CertBinding{
		Thumbprint: thumbprint, Kind: user.CertBindingSPKIThumbprint,
	})
	return nil
}

// NewRealReissueStack builds a REAL ceremony.CeremonyService, already
// initialized single-admin under soPIN, wrapped in the REAL
// service.CeremonyServiceWithAdminRebind -- no PIN-check double anywhere:
// the SO PIN is verified by the REAL pin.SoftwareBackend
// ReissueAdminCredential (via CeremonyServiceWithAdminRebind) reaches
// through pin.RequireProvisionedSOPIN.
//
// This is the one place every transport's "real SO PIN gate" test builds
// this stack (go-qrdb#560 review B), so a change to how the gate is wired
// changes this function rather than five near-identical copies of it. It
// lives in this package, not pkg/ceremony/ceremonytest, because it imports
// pkg/service: ceremonytest is imported by pkg/service's OWN internal
// tests (package service), so a pkg/service import from ceremonytest would
// be an import cycle.
//
// Returns the wrapped CeremonyServicer a transport server wires via
// WithCeremonyService, the CA it wires via WithCeremonyCA, and the admin
// lookup double so a test can inspect CertBindings directly.
func NewRealReissueStack(t testing.TB, soPIN string) (service.CeremonyServicer, ceremony.CeremonyCA, *RealAdminLookup) {
	t.Helper()
	strategy, err := seal.NewSoftwareStrategy([]byte("test-master-password"))
	if err != nil {
		t.Fatalf("NewRealReissueStack: NewSoftwareStrategy: %v", err)
	}
	barrier, err := seal.NewStorageBarrier(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		seal.NewMemoryBackend(), seal.BarrierConfig{RootKeyPath: "test/root-key"}, strategy)
	if err != nil {
		t.Fatalf("NewRealReissueStack: NewStorageBarrier: %v", err)
	}

	tempDir := t.TempDir()
	core, err := ceremony.NewCeremonyService(&ceremony.CeremonyConfig{DataDir: tempDir}, barrier,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewRealReissueStack: NewCeremonyService: %v", err)
	}
	core.SetPINBackend(pintest.NewBackend(t))

	ca, _ := ceremonytest.NewCA(t, "Test Ceremony CA")

	if _, err := core.Initialize(context.Background(), ca, &ceremony.InitializeOptions{
		SOPin: soPIN, UserPin: "test-user-pin", Hostname: "db.example.com", AdminGenerate: true,
	}); err != nil {
		t.Fatalf("NewRealReissueStack: Initialize: %v", err)
	}

	admin := &user.User{ID: 1, Username: user.BootstrapAdminUsername, TenantID: tenantid.System, Lifecycle: user.LifecycleActive}
	lookup := &RealAdminLookup{Admin: admin}
	wrapped := service.NewCeremonyServiceWithAdminRebind(service.NewCeremonyService(core), core, lookup, lookup, nil)
	return wrapped, ca, lookup
}
