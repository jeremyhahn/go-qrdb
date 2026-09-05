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

package sdk

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// newServicerForNotFound builds the production user service the way a node
// does. pkg/user and pkg/dao are imported here ONLY to construct it -- a
// composer receives the servicer from the node and never imports either. The
// classification under test uses nothing but this package's exports.
func newServicerForNotFound(t *testing.T) UserServicer {
	t.Helper()
	idgen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	svc, err := user.NewStore(NewMemoryKVStore(), idgen)
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}
	return svc
}

// TestUserNotFoundError_ServicerPathClassifiableViaSDKType is the go-qrdb#343
// contract: a consumer holding sdk.UserServicer classifies "no such user"
// with sdk.UserNotFoundError -- the ONLY not-found name the SDK exports --
// on every lookup the servicer offers, without importing pkg/user.
func TestUserNotFoundError_ServicerPathClassifiableViaSDKType(t *testing.T) {
	svc := newServicerForNotFound(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name string
		call func() error
		want string // Lookup descriptor, or "" for an id lookup
		id   uint64
	}{
		{"GetUserByEmail", func() error {
			_, err := svc.GetUserByEmail(ctx, "acme", "admin@acme.example")
			return err
		}, "acme/admin@acme.example", 0},
		{"GetUserByUsername", func() error {
			_, err := svc.GetUserByUsername(ctx, "acme", "admin")
			return err
		}, "acme/admin", 0},
		{"GetUser", func() error {
			_, err := svc.GetUser(ctx, 424242)
			return err
		}, "", 424242},
		// The servicer sees the RESOLVED tenant (the default when the
		// context names none), so its descriptor carries it; a remote
		// transport's would not. Match on the type, treat Lookup as
		// diagnostic -- see transport.UserLookupKey.
		{"UserByCertThumbprint", func() error {
			_, err := svc.UserByCertThumbprint(ctx, "deadbeef")
			return err
		}, "cert_spki/system/deadbeef", 0},
		{"DeleteUser", func() error { return svc.DeleteUser(ctx, 424242) }, "", 424242},
		{"DisableUser", func() error { return svc.DisableUser(ctx, 424242) }, "", 424242},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("call on a missing user returned nil")
			}
			var notFound *UserNotFoundError
			if !errors.As(err, &notFound) {
				t.Fatalf("errors.As(err, *sdk.UserNotFoundError) = false for %T: %v", err, err)
			}
			if notFound.Lookup != tc.want || notFound.ID != tc.id {
				t.Errorf("UserNotFoundError{ID: %d, Lookup: %q}, want {%d, %q}", notFound.ID, notFound.Lookup, tc.id, tc.want)
			}
		})
	}
}

// TestUserNotFoundError_OneMessageWhicheverLayerReturnsIt pins that the
// message a consumer logs does not depend on which layer produced the error.
func TestUserNotFoundError_OneMessageWhicheverLayerReturnsIt(t *testing.T) {
	_, svcErr := newServicerForNotFound(t).GetUser(context.Background(), 7)
	if got, want := svcErr.Error(), (&UserNotFoundError{ID: 7}).Error(); got != want {
		t.Errorf("servicer message %q != SDK type message %q", got, want)
	}
}
