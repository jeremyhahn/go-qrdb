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

package embedded

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// countingAuthorizer admits everything and counts the decisions it made.
type countingAuthorizer struct{ calls atomic.Int64 }

func (a *countingAuthorizer) Authorize(context.Context, *auth.Identity, string, string) error {
	a.calls.Add(1)
	return nil
}

// TestNilRequest_RefusedTypedBeforeAuthorizing: a nil request to the user
// and role writes and lists is refused with *transport.RequestError wrapping
// *transport.NilRequestError, before any authorization decision is made or
// charged -- not a panic, and not an authorization against a guessed tenant.
func TestNilRequest_RefusedTypedBeforeAuthorizing(t *testing.T) {
	authz := &countingAuthorizer{}
	tr := newExtTransport(t, WithUserService(&mockUserServicer{}), WithRoleService(&mockRoleServicer{}),
		WithAuthMiddleware(middleware.New(authz)))
	ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "s", TenantID: tenantid.System})

	calls := map[string]func() error{
		"CreateUser": func() error { _, err := tr.CreateUser(ctx, nil); return err },
		"UpdateUser": func() error { _, err := tr.UpdateUser(ctx, nil); return err },
		"UpdateRole": func() error { _, err := tr.UpdateRole(ctx, nil); return err },
		"ListUsers":  func() error { _, err := tr.ListUsers(ctx, nil); return err },
		"ListRoles":  func() error { _, err := tr.ListRoles(ctx, nil); return err },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := func() (err error) {
				defer func() {
					if r := recover(); r != nil {
						err = fmt.Errorf("panic: %v", r)
					}
				}()
				return call()
			}()
			var reqErr *transport.RequestError
			var nilReq *transport.NilRequestError
			if !errors.As(err, &reqErr) || reqErr.Method != name || !errors.As(err, &nilReq) {
				t.Fatalf("%s(nil) = %v (%T), want *transport.RequestError{Method: %q} wrapping *transport.NilRequestError", name, err, err, name)
			}
			if n := authz.calls.Load(); n != 0 {
				t.Fatalf("%s(nil) made %d authorization decisions before refusing", name, n)
			}
		})
	}
}
