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

// The node seal store over the Unix socket: a secret name that is not one
// opaque path segment is the caller's error, answered InvalidArgument.

package unix

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// nameRuleSealStore is a seal store double that enforces only the seal
// store's name rule (validSecretName) and keeps nothing.
type nameRuleSealStore struct{}

func nameRuleErr(op, name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return &seal.SealStoreError{Op: op, Key: name, Err: seal.ErrInvalidSecretName}
	}
	return nil
}

func (nameRuleSealStore) Put(_ context.Context, name string, _ []byte) error {
	return nameRuleErr("put", name)
}

func (nameRuleSealStore) Get(_ context.Context, name string) ([]byte, error) {
	if err := nameRuleErr("get", name); err != nil {
		return nil, err
	}
	return nil, &seal.SealStoreError{Op: "get", Key: name, Err: seal.ErrSealStoreSecretNotFound}
}

func (nameRuleSealStore) Delete(_ context.Context, name string) error {
	return nameRuleErr("delete", name)
}

func (nameRuleSealStore) List(context.Context) ([]string, error) { return nil, nil }

func (nameRuleSealStore) Reseal(_ context.Context, name string) error {
	return nameRuleErr("reseal", name)
}

func (nameRuleSealStore) Status(context.Context) (*seal.SealStoreStatus, error) {
	return &seal.SealStoreStatus{}, nil
}

func TestUnix_SealStore_InvalidNameIsInvalidArgument(t *testing.T) {
	roles := authtest.BuiltInRoles(t, tenantid.System)
	so := authtest.SOIdentity(tenantid.System, "so")
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles), authmw.WithAuthenticator(authtest.NewFixedAuthenticator(so)))
	socketPath := tempSocketPath(t)
	tlsCfg := selfSignedTLSConfig(t)
	srv := NewServer(newMockKVService(), &mockAdminService{}, socketPath,
		WithTLS(tlsCfg), WithAuthMiddleware(mw), WithSealStoreService(nameRuleSealStore{}))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)
	conn := dialUnixTLS(t, socketPath, tlsCfg)
	t.Cleanup(func() { _ = conn.Close() })

	c := pb.NewQRDBServiceClient(conn)
	_, err := c.SealStorePut(context.Background(), &pb.SealStorePutRequest{Name: `a\b`, Secret: []byte("s1")})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("Put(a\\b): code %s (%v), want InvalidArgument", status.Code(err), err)
	}
}
