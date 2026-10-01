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

// No build constraint: the stub is shared by handlers_db_barrier_custodian_test.go
// (!integration), route_mapping_guard_test.go and node_officer_scope_test.go,
// so its constraint is the union of theirs.

package rest

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
)

type stubCustodianSvc struct {
	registerFn func(ctx context.Context, req custodian.RegisterRequest) (*custodian.RegisterResponse, error)
	getFn      func(ctx context.Context, id string) (*custodian.Custodian, error)
	listFn     func(ctx context.Context, tenantID string) ([]*custodian.Custodian, error)
	revokeFn   func(ctx context.Context, id string) error
}

var _ custodianServicer = (*stubCustodianSvc)(nil)

func newStubCustodianSvc() *stubCustodianSvc {
	return &stubCustodianSvc{
		registerFn: func(_ context.Context, req custodian.RegisterRequest) (*custodian.RegisterResponse, error) {
			return &custodian.RegisterResponse{
				Custodian:      &custodian.Custodian{ID: "cust-1", Username: req.Username},
				SealedEnvelope: nil,
			}, nil
		},
		getFn: func(_ context.Context, _ string) (*custodian.Custodian, error) {
			return &custodian.Custodian{ID: "c1"}, nil
		},
		listFn:   func(_ context.Context, _ string) ([]*custodian.Custodian, error) { return nil, nil },
		revokeFn: func(_ context.Context, _ string) error { return nil },
	}
}

func (s *stubCustodianSvc) RegisterCustodian(ctx context.Context, req custodian.RegisterRequest) (*custodian.RegisterResponse, error) {
	return s.registerFn(ctx, req)
}
func (s *stubCustodianSvc) GetCustodian(ctx context.Context, id string) (*custodian.Custodian, error) {
	return s.getFn(ctx, id)
}
func (s *stubCustodianSvc) ListCustodians(ctx context.Context, tenantID string) ([]*custodian.Custodian, error) {
	return s.listFn(ctx, tenantID)
}
func (s *stubCustodianSvc) RevokeCustodian(ctx context.Context, id string) error {
	return s.revokeFn(ctx, id)
}
