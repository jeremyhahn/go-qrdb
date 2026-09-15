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

	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// mock_user_service_test.go holds mockUserService, the package's shared
// service.UserServicer double.
//
// It carries NO build constraint on purpose. The double used to live in
// grpc_auth_test.go, which is `!integration`, while callers sat under
// `qrdb_ts` and under no constraint at all — so under BUILD_TAGS+integration
// the type vanished and the package stopped typechecking (#203, and the same
// shape as #178). A test helper's constraint must be a SUPERSET of every
// caller's; the union of this one's callers is unconstrained, so this file is
// too. Do not give it a constraint to "match" the file it came from — that is
// exactly the bug.

// The compile-time check belongs with the double: it is what turns "a method
// slipped behind a build constraint" into a failure HERE rather than at some
// construction site in another file (#203).
var _ user.UserServicer = (*mockUserService)(nil)

type mockUserService struct {
	createResult      *user.User
	createErr         error
	getResult         *user.User
	getErr            error
	getByUsernameErr  error
	getByEmailErr     error
	updateResult      *user.User
	updateErr         error
	deleteErr         error
	listResult        *user.ListUsersResponse
	listErr           error
	enableErr         error
	disableErr        error
	lastUpdateRoleIDs []string
	lastGetID         uint64
	lastCreateReq     *user.CreateUserRequest
	lastUpdateReq     *user.UpdateUserRequest

	addCertBindingFn    func(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*user.User, error)
	removeCertBindingFn func(ctx context.Context, userID uint64, thumbprint string) (*user.User, error)
}

func (m *mockUserService) CreateUser(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
	m.lastCreateReq = req
	if m.createErr != nil {
		return nil, m.createErr
	}
	return m.createResult, nil
}

func (m *mockUserService) GetUser(_ context.Context, id uint64) (*user.User, error) {
	m.lastGetID = id
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.getResult, nil
}

func (m *mockUserService) GetUserByUsername(_ context.Context, _, _ string) (*user.User, error) {
	return nil, m.getByUsernameErr
}

func (m *mockUserService) GetUserByEmail(_ context.Context, _, _ string) (*user.User, error) {
	return nil, m.getByEmailErr
}

func (m *mockUserService) UpdateUser(_ context.Context, req *user.UpdateUserRequest) (*user.User, error) {
	if m.updateErr != nil {
		return nil, m.updateErr
	}
	m.lastUpdateRoleIDs = req.RoleIDs
	m.lastUpdateReq = req
	return m.updateResult, nil
}

func (m *mockUserService) DeleteUser(_ context.Context, _ uint64) error {
	return m.deleteErr
}

func (m *mockUserService) ListUsers(_ context.Context, _ *user.ListUsersRequest) (*user.ListUsersResponse, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.listResult, nil
}

func (m *mockUserService) EnableUser(_ context.Context, _ uint64) error {
	return m.enableErr
}

func (m *mockUserService) DisableUser(_ context.Context, _ uint64) error {
	return m.disableErr
}

func (m *mockUserService) UserByCertThumbprint(_ context.Context, _ string) (*user.User, error) {
	return nil, nil
}

func (m *mockUserService) AddCertBinding(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*user.User, error) {
	if m.addCertBindingFn != nil {
		return m.addCertBindingFn(ctx, userID, thumbprint, notAfterUnix)
	}
	return nil, nil
}

func (m *mockUserService) RemoveCertBinding(ctx context.Context, userID uint64, thumbprint string) (*user.User, error) {
	if m.removeCertBindingFn != nil {
		return m.removeCertBindingFn(ctx, userID, thumbprint)
	}
	return nil, nil
}
