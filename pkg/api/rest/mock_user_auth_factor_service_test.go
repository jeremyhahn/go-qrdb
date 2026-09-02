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

package rest

import (
	"context"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mock_user_auth_factor_service_test.go holds mockUserAuthFactorService, the
// package's shared service.UserAuthFactorServicer double.
//
// It carries NO build constraint on purpose. The double used to live in
// rest_new_coverage_test.go, which is `!integration`, while
// user_auth_factor_login_enrich_test.go consumes it under no constraint at all.
// Under BUILD_TAGS+integration the type vanished and the package stopped
// typechecking (#203, the shape #178 first hit). A test helper's constraint
// must be a SUPERSET of every caller's; the union of this one's callers is
// unconstrained, so this file is too.

type mockUserAuthFactorService struct {
	addFn                func(ctx context.Context, userID uint64, factor *auth.AuthFactor) error
	listFn               func(ctx context.Context, userID uint64) ([]*auth.AuthFactor, error)
	getFn                func(ctx context.Context, factorID string) (*auth.AuthFactor, error)
	lookupByCredentialFn func(ctx context.Context, t auth.AuthFactorType, key []byte) (*auth.AuthFactor, error)
	removeFn             func(ctx context.Context, factorID string, force bool) error
	rotateFn             func(ctx context.Context, oldID string, newFactor *auth.AuthFactor) error
	relabelFn            func(ctx context.Context, factorID string, label string) error
	markUsedFn           func(ctx context.Context, factorID string, ts time.Time) error
}

var _ service.UserAuthFactorServicer = (*mockUserAuthFactorService)(nil)

func (m *mockUserAuthFactorService) Add(ctx context.Context, userID uint64, factor *auth.AuthFactor) error {
	if m.addFn != nil {
		return m.addFn(ctx, userID, factor)
	}
	return nil
}

func (m *mockUserAuthFactorService) List(ctx context.Context, userID uint64) ([]*auth.AuthFactor, error) {
	if m.listFn != nil {
		return m.listFn(ctx, userID)
	}
	return nil, nil
}

func (m *mockUserAuthFactorService) Get(ctx context.Context, factorID string) (*auth.AuthFactor, error) {
	if m.getFn != nil {
		return m.getFn(ctx, factorID)
	}
	return &auth.AuthFactor{ID: factorID}, nil
}

func (m *mockUserAuthFactorService) LookupByCredentialKey(ctx context.Context, t auth.AuthFactorType, key []byte) (*auth.AuthFactor, error) {
	if m.lookupByCredentialFn != nil {
		return m.lookupByCredentialFn(ctx, t, key)
	}
	return &auth.AuthFactor{ID: "factor-1", UserID: 42, Type: t}, nil
}

func (m *mockUserAuthFactorService) Remove(ctx context.Context, factorID string, force bool) error {
	if m.removeFn != nil {
		return m.removeFn(ctx, factorID, force)
	}
	return nil
}

func (m *mockUserAuthFactorService) Rotate(ctx context.Context, oldID string, newFactor *auth.AuthFactor) error {
	if m.rotateFn != nil {
		return m.rotateFn(ctx, oldID, newFactor)
	}
	return nil
}

func (m *mockUserAuthFactorService) Relabel(ctx context.Context, factorID string, label string) error {
	if m.relabelFn != nil {
		return m.relabelFn(ctx, factorID, label)
	}
	return nil
}

func (m *mockUserAuthFactorService) MarkUsed(ctx context.Context, factorID string, ts time.Time) error {
	if m.markUsedFn != nil {
		return m.markUsedFn(ctx, factorID, ts)
	}
	return nil
}
