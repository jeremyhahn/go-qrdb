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

// iamVocabulary maps every IAM vocabulary SDK name to the pkg/permission
// constant it must equal.
var iamVocabulary = map[string]struct {
	sdkName permission.Permission
	want    permission.Permission
}{
	"PermissionKeysSign":    {sdk.PermissionKeysSign, permission.KeysSign},
	"PermissionKeysVerify":  {sdk.PermissionKeysVerify, permission.KeysVerify},
	"PermissionKeysEncrypt": {sdk.PermissionKeysEncrypt, permission.KeysEncrypt},
	"PermissionKeysDecrypt": {sdk.PermissionKeysDecrypt, permission.KeysDecrypt},
	"PermissionKeysWrap":    {sdk.PermissionKeysWrap, permission.KeysWrap},
	"PermissionKeysUnwrap":  {sdk.PermissionKeysUnwrap, permission.KeysUnwrap},
	"PermissionKeysCreate":  {sdk.PermissionKeysCreate, permission.KeysCreate},
	"PermissionKeysRotate":  {sdk.PermissionKeysRotate, permission.KeysRotate},
	"PermissionKeysDelete":  {sdk.PermissionKeysDelete, permission.KeysDelete},
	"PermissionKeysList":    {sdk.PermissionKeysList, permission.KeysList},
	"PermissionKeysGet":     {sdk.PermissionKeysGet, permission.KeysGet},
	"PermissionKeysImport":  {sdk.PermissionKeysImport, permission.KeysImport},
	"PermissionKeysExport":  {sdk.PermissionKeysExport, permission.KeysExport},
	"PermissionKeysDerive":  {sdk.PermissionKeysDerive, permission.KeysDerive},

	"PermissionCertsIssue":    {sdk.PermissionCertsIssue, permission.CertsIssue},
	"PermissionCertsEnroll":   {sdk.PermissionCertsEnroll, permission.CertsEnroll},
	"PermissionCertsDelegate": {sdk.PermissionCertsDelegate, permission.CertsDelegate},
	"PermissionCertsRevoke":   {sdk.PermissionCertsRevoke, permission.CertsRevoke},
	"PermissionCertsList":     {sdk.PermissionCertsList, permission.CertsList},
	"PermissionCertsRead":     {sdk.PermissionCertsRead, permission.CertsRead},

	"PermissionPasswordsRead":              {sdk.PermissionPasswordsRead, permission.PasswordsRead},
	"PermissionPasswordsWrite":             {sdk.PermissionPasswordsWrite, permission.PasswordsWrite},
	"PermissionPasswordsDelete":            {sdk.PermissionPasswordsDelete, permission.PasswordsDelete},
	"PermissionPasswordsGenerate":          {sdk.PermissionPasswordsGenerate, permission.PasswordsGenerate},
	"PermissionPasswordsLock":              {sdk.PermissionPasswordsLock, permission.PasswordsLock},
	"PermissionPasswordsUnlock":            {sdk.PermissionPasswordsUnlock, permission.PasswordsUnlock},
	"PermissionPasswordsOverrideOwnership": {sdk.PermissionPasswordsOverrideOwnership, permission.PasswordsOverrideOwnership},

	"PermissionSSHRead":   {sdk.PermissionSSHRead, permission.SSHRead},
	"PermissionSSHWrite":  {sdk.PermissionSSHWrite, permission.SSHWrite},
	"PermissionSSHDelete": {sdk.PermissionSSHDelete, permission.SSHDelete},

	"PermissionPINRead":   {sdk.PermissionPINRead, permission.PINRead},
	"PermissionPINWrite":  {sdk.PermissionPINWrite, permission.PINWrite},
	"PermissionPIVRead":   {sdk.PermissionPIVRead, permission.PIVRead},
	"PermissionPIVWrite":  {sdk.PermissionPIVWrite, permission.PIVWrite},
	"PermissionPIVUse":    {sdk.PermissionPIVUse, permission.PIVUse},
	"PermissionPIVDelete": {sdk.PermissionPIVDelete, permission.PIVDelete},

	"PermissionDevicesOnboard": {sdk.PermissionDevicesOnboard, permission.DevicesOnboard},
	"PermissionDevicePair":     {sdk.PermissionDevicePair, permission.DevicePair},
	"PermissionDeviceRevoke":   {sdk.PermissionDeviceRevoke, permission.DeviceRevoke},
	"PermissionDeviceShare":    {sdk.PermissionDeviceShare, permission.DeviceShare},
	"PermissionDeviceSync":     {sdk.PermissionDeviceSync, permission.DeviceSync},
}

// TestSDK_IAMVocabulary_AliasesMatchRegistry pins that every SDK name equals
// its pkg/permission constant and names a registered permission.
func TestSDK_IAMVocabulary_AliasesMatchRegistry(t *testing.T) {
	for name, tc := range iamVocabulary {
		if tc.sdkName != tc.want {
			t.Errorf("sdk.%s = %q, want %q", name, tc.sdkName, tc.want)
		}
		if !permission.IsRegistered(tc.sdkName) {
			t.Errorf("sdk.%s (%q) is not registered", name, tc.sdkName)
		}
	}
}

// TestSDK_IAMVocabulary_AreComplete pins that every self-scope permission of
// the IAM resource families has an SDK name, so a permission added to one of
// them later without an SDK alias fails here. Cross-scope members
// (certs:revoke:cross) are covered by types_permission_cross.go.
func TestSDK_IAMVocabulary_AreComplete(t *testing.T) {
	named := make(map[permission.Permission]struct{}, len(iamVocabulary))
	for _, tc := range iamVocabulary {
		named[tc.sdkName] = struct{}{}
	}
	for _, resource := range []string{"keys", "certs", "passwords", "ssh", "pin", "piv", "devices", "device"} {
		members := permission.ByResource(resource)
		if len(members) == 0 {
			t.Errorf("ByResource(%q): no permissions", resource)
		}
		for _, p := range members {
			if permission.IsCrossScope(p) {
				continue
			}
			if _, ok := named[p]; !ok {
				t.Errorf("%q (resource %q) has no sdk.Permission* name", p, resource)
			}
		}
	}
}
