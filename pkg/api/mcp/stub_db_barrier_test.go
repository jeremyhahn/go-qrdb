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

package mcp

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// stubMCPDBWithBarrier is a database service double with controllable
// barrier methods. It carries no build constraint because its callers span
// tag sets: the barrier tool tests and the secret-argument tests.
type stubMCPDBWithBarrier struct {
	mockDatabaseService
	sealFn          func(ctx context.Context, name string) error
	unsealFn        func(ctx context.Context, name string, creds seal.Credentials) error
	barrierStatusFn func(name string) (*seal.BarrierStatus, error)
}

var _ service.DatabaseServicer = (*stubMCPDBWithBarrier)(nil)

func newStubMCPDBWithBarrier() *stubMCPDBWithBarrier {
	return &stubMCPDBWithBarrier{
		sealFn:   func(_ context.Context, _ string) error { return nil },
		unsealFn: func(_ context.Context, _ string, _ seal.Credentials) error { return nil },
		barrierStatusFn: func(_ string) (*seal.BarrierStatus, error) {
			return &seal.BarrierStatus{
				Sealed:   false,
				Strategy: "software",
			}, nil
		},
	}
}

func (s *stubMCPDBWithBarrier) SealDatabase(ctx context.Context, name string) error {
	return s.sealFn(ctx, name)
}
func (s *stubMCPDBWithBarrier) UnsealDatabase(ctx context.Context, name string, creds seal.Credentials) error {
	return s.unsealFn(ctx, name, creds)
}

// DatabaseBarrierStatus adapts databaseBarrierStatusSeal to the service
// interface, with no cluster seal state.
func (s *stubMCPDBWithBarrier) DatabaseBarrierStatus(ctx context.Context, name string) (*service.DatabaseBarrierStatusInfo, error) {
	st, err := s.databaseBarrierStatusSeal(ctx, name)
	if st == nil {
		return nil, err
	}
	return &service.DatabaseBarrierStatusInfo{BarrierStatus: st}, err
}

func (s *stubMCPDBWithBarrier) databaseBarrierStatusSeal(_ context.Context, name string) (*seal.BarrierStatus, error) {
	return s.barrierStatusFn(name)
}
