// Copyright 2026 Jeremy Hahn
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

package grpc

// mockUserAuthFactorSvc is shared across the gRPC test suite. It lives in an
// UNTAGGED file so it is available under every build-tag configuration that
// compiles the package's tests — in particular both the default (`!qrdb_ts`)
// build and the full tag set that includes `qrdb_ts`. register_user_auth_factor_test.go
// (also untagged) consumes it; keeping the definition here avoids the
// tag-gated "undefined mock" trap that arises when a helper is defined in a
// `!qrdb_ts`-constrained file but used from an untagged one.

import (
	"context"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

type mockUserAuthFactorSvc struct {
	addErr       error
	listResult   []*auth.AuthFactor
	listErr      error
	getResult    *auth.AuthFactor
	getErr       error
	removeErr    error
	deleteErr    error
	rotateErr    error
	relabelErr   error
	lookupResult *auth.AuthFactor
	lookupErr    error

	// condVersion and condErr answer AddConditional; gotCond records the
	// condition it was called with.
	condVersion uint64
	condErr     error
	gotCond     *auth.FactorAddCondition
	// setVersion and setVersionErr answer FactorSetVersion.
	setVersion    uint64
	setVersionErr error
}

var _ service.UserAuthFactorServicer = (*mockUserAuthFactorSvc)(nil)

func newMockUserAuthFactorSvc() *mockUserAuthFactorSvc {
	now := time.Now()
	return &mockUserAuthFactorSvc{
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

func (m *mockUserAuthFactorSvc) Add(_ context.Context, _ uint64, _ *auth.AuthFactor) error {
	return m.addErr
}
func (m *mockUserAuthFactorSvc) List(_ context.Context, _ uint64) ([]*auth.AuthFactor, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listResult, nil
}
func (m *mockUserAuthFactorSvc) Get(_ context.Context, _ string) (*auth.AuthFactor, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.getResult, nil
}
func (m *mockUserAuthFactorSvc) LookupByCredentialKey(_ context.Context, _ auth.AuthFactorType, _ []byte) (*auth.AuthFactor, error) {
	if m.lookupErr != nil {
		return nil, m.lookupErr
	}
	return m.lookupResult, nil
}
func (m *mockUserAuthFactorSvc) Remove(_ context.Context, _ string, _ bool) error {
	return m.removeErr
}
func (m *mockUserAuthFactorSvc) Delete(_ context.Context, _ string, _ bool) error {
	return m.deleteErr
}
func (m *mockUserAuthFactorSvc) Rotate(_ context.Context, _ string, _ *auth.AuthFactor) error {
	return m.rotateErr
}
func (m *mockUserAuthFactorSvc) Relabel(_ context.Context, _ string, _ string) error {
	return m.relabelErr
}
func (m *mockUserAuthFactorSvc) MarkUsed(_ context.Context, _ string, _ time.Time) error {
	return nil
}

func (m *mockUserAuthFactorSvc) AddConditional(_ context.Context, _ uint64, _ *auth.AuthFactor, cond auth.FactorAddCondition) (uint64, error) {
	m.gotCond = &cond
	return m.condVersion, m.condErr
}

func (m *mockUserAuthFactorSvc) FactorSetVersion(_ context.Context, _ uint64) (uint64, error) {
	return m.setVersion, m.setVersionErr
}
