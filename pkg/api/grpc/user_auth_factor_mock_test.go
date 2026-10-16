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

package grpc

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// fullMockUserAuthFactorSvc implements service.UserAuthFactorServicer.
type fullMockUserAuthFactorSvc struct {
	addErr       error
	listResult   []*auth.AuthFactor
	listErr      error
	getResult    *auth.AuthFactor
	getErr       error
	removeErr    error
	deleteErr    error
	deletedID    string
	deletedForce bool
	rotateErr    error
	relabelErr   error
	lookupResult *auth.AuthFactor
	lookupErr    error
}

var _ service.UserAuthFactorServicer = (*fullMockUserAuthFactorSvc)(nil)

func (m *fullMockUserAuthFactorSvc) Add(_ context.Context, _ uint64, _ *auth.AuthFactor) error {
	return m.addErr
}
func (m *fullMockUserAuthFactorSvc) List(_ context.Context, _ uint64) ([]*auth.AuthFactor, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listResult, nil
}
func (m *fullMockUserAuthFactorSvc) Get(_ context.Context, _ string) (*auth.AuthFactor, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.getResult, nil
}
func (m *fullMockUserAuthFactorSvc) LookupByCredentialKey(_ context.Context, _ auth.AuthFactorType, _ []byte) (*auth.AuthFactor, error) {
	if m.lookupErr != nil {
		return nil, m.lookupErr
	}
	return m.lookupResult, nil
}
func (m *fullMockUserAuthFactorSvc) Remove(_ context.Context, _ string, _ bool) error {
	return m.removeErr
}
func (m *fullMockUserAuthFactorSvc) Delete(_ context.Context, factorID string, force bool) error {
	m.deletedID, m.deletedForce = factorID, force
	return m.deleteErr
}
func (m *fullMockUserAuthFactorSvc) Rotate(_ context.Context, _ string, _ *auth.AuthFactor) error {
	return m.rotateErr
}
func (m *fullMockUserAuthFactorSvc) Relabel(_ context.Context, _ string, _ string) error {
	return m.relabelErr
}
func (m *fullMockUserAuthFactorSvc) MarkUsed(_ context.Context, _ string, _ time.Time) error {
	return nil
}

func (m *fullMockUserAuthFactorSvc) AddConditional(_ context.Context, _ uint64, _ *auth.AuthFactor, _ auth.FactorAddCondition) (uint64, error) {
	return 0, m.addErr
}

func (m *fullMockUserAuthFactorSvc) FactorSetVersion(_ context.Context, _ uint64) (uint64, error) {
	return 0, nil
}

func newFullMockUserAuthFactorSvc() *fullMockUserAuthFactorSvc {
	now := time.Now()
	return &fullMockUserAuthFactorSvc{
		getResult: &auth.AuthFactor{
			ID:           "factor-1",
			UserID:       42,
			Type:         auth.AuthFactorType("totp"),
			Label:        "my-totp",
			Credential:   []byte("secret"),
			RegisteredAt: now,
		},
		lookupResult: &auth.AuthFactor{
			ID:           "factor-1",
			UserID:       42,
			Type:         auth.AuthFactorType("totp"),
			RegisteredAt: now,
		},
		listResult: []*auth.AuthFactor{
			{ID: "factor-1", UserID: 42, Type: "totp", RegisteredAt: now},
		},
	}
}

// startServerWithUserAuthFactorFull starts a gRPC server wired with the
// UserAuthFactor service. It is equivalent to startServerWithUserAuthFactor
// in grpc_coverage2_test.go but has no build tag restriction.
func startServerWithUserAuthFactorFull(t *testing.T, svc service.UserAuthFactorServicer) (*grpc.ClientConn, func()) {
	t.Helper()
	tlsCfg := testServerTLSConfig(t)
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithTLS(tlsCfg), WithUserAuthFactorService(svc))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	conn, err := grpc.NewClient(srv.Addr(), grpc.WithTransportCredentials(testClientCreds(t, tlsCfg)))
	if err != nil {
		srv.Stop()
		t.Fatalf("grpc.NewClient failed: %v", err)
	}
	return conn, func() {
		_ = conn.Close()
		srv.Stop()
	}
}
