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

package conformance

import (
	"bytes"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// The tenant DEK keyring conformance matrix.
//
// Every transport carries the six keyring verbs, and the promise is that they
// behave IDENTICALLY however the keyring is reached. Each cell drives the
// whole lifecycle over ONE transport against a real node, through the real
// authentication and RBAC stack on the remote cells: encrypt, rotate (the old
// ciphertext still decrypts), rewrap, retire the old version (its ciphertext
// stops decrypting, the rewrapped one does not), plus the three refusals —
// a mismatched AAD, retiring the current version, and naming another tenant.

// seedDEKIdentity grants the "matrix" identity the keyring permissions and
// returns the role id so the embedded cell, which authenticates nothing on
// its own, can bind the same identity into its own context.
func seedDEKIdentity(t *testing.T, n *node.Node) (roleID string) {
	t.Helper()
	svc := n.Services()
	ctx := boundedCtx(t)
	r, err := svc.Role.CreateRole(ctx, &role.CreateRoleRequest{
		Name:     "dek-matrix",
		TenantID: tenantid.System,
		Permissions: []permission.Permission{
			permission.DEKEncrypt, permission.DEKDecrypt, permission.DEKRewrap,
			permission.DEKRotate, permission.DEKRetire, permission.DEKStatus,
			permission.AdminHealth, permission.AdminStatus,
		},
	})
	if err != nil {
		t.Fatalf("create role: %v", err)
	}
	if _, err := svc.User.CreateUser(ctx, &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             "matrix",
		TenantID:             tenantid.System,
		RoleIDs:              []string{r.ID},
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return r.ID
}

func TestTenantDEK_LifecycleOnEveryTransport(t *testing.T) {
	for _, tk := range transportKinds {
		t.Run(string(tk), func(t *testing.T) {
			var roleID string
			c := bootAuthorizedCell(t, tk, func(n *node.Node) { roleID = seedDEKIdentity(t, n) }).DEK()
			ctx := boundedCtx(t)
			if tk == embeddedTransport {
				ctx = tenant.WithTenantID(auth.WithIdentity(ctx, &auth.Identity{
					Subject:  "matrix",
					TenantID: tenantid.System,
					Roles:    []string{roleID},
				}), tenantid.System)
			}
			aad := []byte("system/app/secret/v1")

			enc, err := c.TenantDEKEncrypt(ctx, &transport.TenantDEKEncryptRequest{Plaintext: []byte("s3cret"), AAD: aad})
			if err != nil {
				t.Fatalf("%s: encrypt: %v", tk, err)
			}
			if enc.Version != 1 {
				t.Fatalf("%s: first encryption under v%d, want v1", tk, enc.Version)
			}

			st, err := c.TenantDEKRotate(ctx, &transport.TenantDEKRequest{})
			if err != nil || st.CurrentVersion != 2 {
				t.Fatalf("%s: rotate = %+v, %v", tk, st, err)
			}
			dec, err := c.TenantDEKDecrypt(ctx, &transport.TenantDEKDecryptRequest{Ciphertext: enc.Ciphertext, AAD: aad})
			if err != nil || !bytes.Equal(dec.Plaintext, []byte("s3cret")) || dec.Version != 1 {
				t.Fatalf("%s: v1 after rotate = %+v, %v", tk, dec, err)
			}

			rw, err := c.TenantDEKRewrap(ctx, &transport.TenantDEKRewrapRequest{Ciphertext: enc.Ciphertext, AAD: aad})
			if err != nil || rw.FromVersion != 1 || rw.ToVersion != 2 {
				t.Fatalf("%s: rewrap = %+v, %v", tk, rw, err)
			}

			if _, err := c.TenantDEKDecrypt(ctx, &transport.TenantDEKDecryptRequest{Ciphertext: rw.Ciphertext, AAD: []byte("other")}); err == nil {
				t.Errorf("%s: a mismatched AAD decrypted", tk)
			}
			if _, err := c.TenantDEKRetire(ctx, &transport.TenantDEKRetireRequest{Version: 2}); err == nil {
				t.Errorf("%s: the current version was retired", tk)
			}
			if _, err := c.TenantDEKStatus(ctx, &transport.TenantDEKRequest{TenantID: "someone-else"}); err == nil {
				t.Errorf("%s: another tenant's keyring was reachable", tk)
			}

			if st, err = c.TenantDEKRetire(ctx, &transport.TenantDEKRetireRequest{Version: 1}); err != nil || st.Versions[0].RetiredAt == nil {
				t.Fatalf("%s: retire v1 = %+v, %v", tk, st, err)
			}
			if _, err := c.TenantDEKDecrypt(ctx, &transport.TenantDEKDecryptRequest{Ciphertext: enc.Ciphertext, AAD: aad}); err == nil {
				t.Errorf("%s: a retired version still decrypts", tk)
			}
			dec, err = c.TenantDEKDecrypt(ctx, &transport.TenantDEKDecryptRequest{Ciphertext: rw.Ciphertext, AAD: aad})
			if err != nil || !bytes.Equal(dec.Plaintext, []byte("s3cret")) {
				t.Fatalf("%s: rewrapped ciphertext after retire = %+v, %v", tk, dec, err)
			}
			if st, err = c.TenantDEKStatus(ctx, &transport.TenantDEKRequest{}); err != nil || st.TenantID != tenantid.System || len(st.Versions) != 2 {
				t.Fatalf("%s: status = %+v, %v", tk, st, err)
			}
		})
	}
}

// TestTenantDEK_PermissionIsEnforcedOnEveryRemoteTransport: an identity
// without the dek:* grants is refused over the wire, so the method-level
// permission is wired on every transport rather than defaulting open.
func TestTenantDEK_PermissionIsEnforcedOnEveryRemoteTransport(t *testing.T) {
	for _, tk := range transportKinds {
		if !tk.remote() {
			continue
		}
		t.Run(string(tk), func(t *testing.T) {
			c := bootAuthorizedCell(t, tk, func(n *node.Node) { seedGuardedIdentity(t, n) }).DEK()
			_, err := c.TenantDEKEncrypt(boundedCtx(t), &transport.TenantDEKEncryptRequest{Plaintext: []byte("x")})
			if err == nil {
				t.Fatalf("%s: an identity without dek:encrypt encrypted", tk)
			}
			// The refusal must be the RBAC decision on dek:encrypt, not some
			// other failure that happens to be an error.
			if !strings.Contains(err.Error(), "cannot encrypt on dek") {
				t.Fatalf("%s: refused for the wrong reason: %v", tk, err)
			}
		})
	}
}
