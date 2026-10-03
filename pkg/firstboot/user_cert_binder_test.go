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

package firstboot

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// testThumbprint returns a syntactically canonical (but not cryptographically
// meaningful) SPKI thumbprint for a test fixture.
func testThumbprint(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Compile-time interface check.
var _ CertBinder = UserCertBinder{}

func newBinderTestUserStore(t *testing.T) user.UserServicer {
	t.Helper()
	gen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	svc, err := user.NewStore(kvstore.NewMemoryKVStore(), gen)
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}
	return svc
}

// TestUserCertBinder_BindsThumbprint covers the happy path: the thumbprint
// lands on the user record with no expiry.
func TestUserCertBinder_BindsThumbprint(t *testing.T) {
	ctx := context.Background()
	svc := newBinderTestUserStore(t)
	u, err := svc.CreateUser(ctx, &user.CreateUserRequest{
		Username: "admin", TenantID: "system", WithoutEmailLoginKey: true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	binder := UserCertBinder{Users: svc}
	if err := binder.BindAdminCert(ctx, "system", u.ID, testThumbprint("thumb-1")); err != nil {
		t.Fatalf("BindAdminCert: %v", err)
	}

	got, err := svc.GetUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if len(got.CertBindings) != 1 || got.CertBindings[0].Thumbprint != testThumbprint("thumb-1") {
		t.Fatalf("CertBindings = %+v, want one binding for thumb-1", got.CertBindings)
	}
	if !got.CertBindings[0].NotAfter.IsZero() {
		t.Fatalf("NotAfter = %v, want zero (never expires)", got.CertBindings[0].NotAfter)
	}
}

// TestUserCertBinder_PropagatesStoreError covers the failure path: a bind
// against a nonexistent user surfaces the store's error unchanged.
func TestUserCertBinder_PropagatesStoreError(t *testing.T) {
	svc := newBinderTestUserStore(t)
	binder := UserCertBinder{Users: svc}
	if err := binder.BindAdminCert(context.Background(), "system", 99999, testThumbprint("thumb-1")); err == nil {
		t.Fatal("expected an error binding a nonexistent user")
	}
}
