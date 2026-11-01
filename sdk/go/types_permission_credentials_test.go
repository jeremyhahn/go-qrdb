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

package sdk_test

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// sdkCredentialsPermissions pairs each SDK re-export of a named-credential
// permission with the canonical value it re-exports.
var sdkCredentialsPermissions = map[sdk.Permission]permission.Permission{
	sdk.PermissionCredentialsSubmit:          permission.CredentialsSubmit,
	sdk.PermissionCredentialsStrategy:        permission.CredentialsStrategy,
	sdk.PermissionCredentialsList:            permission.CredentialsList,
	sdk.PermissionCredentialsDelete:          permission.CredentialsDelete,
	sdk.PermissionCredentialsRotate:          permission.CredentialsRotate,
	sdk.PermissionCredentialsGCExpired:       permission.CredentialsGCExpired,
	sdk.PermissionCredentialsRetrieveVersion: permission.CredentialsRetrieveVersion,
	sdk.PermissionCredentialsListVersions:    permission.CredentialsListVersions,
	sdk.PermissionCredentialsStore:           permission.CredentialsStore,
	sdk.PermissionCredentialsRetrieve:        permission.CredentialsRetrieve,
}

// TestSDK_CredentialsPermissions_AreTheCanonicalOnes proves each re-export is
// the registered canonical value, and that none shares a value with another.
func TestSDK_CredentialsPermissions_AreTheCanonicalOnes(t *testing.T) {
	if len(sdkCredentialsPermissions) != 10 {
		t.Fatalf("%d distinct re-exports, want 10: two SDK names share a value", len(sdkCredentialsPermissions))
	}
	for sdkValue, pkgValue := range sdkCredentialsPermissions {
		if sdkValue != pkgValue {
			t.Errorf("sdk %q != pkg %q", sdkValue, pkgValue)
		}
		if !sdk.IsPermissionRegistered(sdkValue) {
			t.Errorf("%q is not registered", sdkValue)
		}
	}
}

// TestSDK_CredentialsPermissions_AreSelfScope proves no credential permission
// is cross-scope: a credential call acts on the caller's own tenant's store,
// so a grant inside a tenant never reaches another tenant's.
func TestSDK_CredentialsPermissions_AreSelfScope(t *testing.T) {
	for sdkValue := range sdkCredentialsPermissions {
		if permission.IsCrossScope(sdkValue) {
			t.Errorf("%q is cross-scope; credentials are per tenant", sdkValue)
		}
	}
}
