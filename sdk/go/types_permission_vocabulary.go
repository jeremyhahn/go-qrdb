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

package sdk

import "github.com/jeremyhahn/go-qrdb/pkg/permission"

// The IAM vocabulary a consumer such as go-xkms checks against the PDP: the
// keys, certs, passwords, ssh, pin, piv and device permissions qrdb
// registers. Each name is the pkg/permission constant with a Permission
// prefix, so a consumer needs no import of pkg/permission. Every one is
// self-scope except certs:revoke:cross, which is in types_permission_cross.go.
// A permission only one consumer defines (an xkms-only verb) stays that
// module's own registration (permission.RegisterPermission and friends), not
// an SDK name.

// Key permissions.
const (
	// PermissionKeysSign ("keys:sign") signs with a key.
	PermissionKeysSign = permission.KeysSign
	// PermissionKeysVerify ("keys:verify") verifies a signature.
	PermissionKeysVerify = permission.KeysVerify
	// PermissionKeysEncrypt ("keys:encrypt") encrypts with a key.
	PermissionKeysEncrypt = permission.KeysEncrypt
	// PermissionKeysDecrypt ("keys:decrypt") decrypts with a key.
	PermissionKeysDecrypt = permission.KeysDecrypt
	// PermissionKeysWrap ("keys:wrap") wraps key material.
	PermissionKeysWrap = permission.KeysWrap
	// PermissionKeysUnwrap ("keys:unwrap") unwraps key material.
	PermissionKeysUnwrap = permission.KeysUnwrap
	// PermissionKeysCreate ("keys:create") creates a key.
	PermissionKeysCreate = permission.KeysCreate
	// PermissionKeysRotate ("keys:rotate") rotates a key.
	PermissionKeysRotate = permission.KeysRotate
	// PermissionKeysDelete ("keys:delete") deletes a key.
	PermissionKeysDelete = permission.KeysDelete
	// PermissionKeysList ("keys:list") lists keys.
	PermissionKeysList = permission.KeysList
	// PermissionKeysGet ("keys:get") reads a key's metadata.
	PermissionKeysGet = permission.KeysGet
	// PermissionKeysImport ("keys:import") imports key material.
	PermissionKeysImport = permission.KeysImport
	// PermissionKeysExport ("keys:export") exports key material.
	PermissionKeysExport = permission.KeysExport
	// PermissionKeysDerive ("keys:derive") derives a key.
	PermissionKeysDerive = permission.KeysDerive
)

// Certificate permissions.
const (
	// PermissionCertsIssue ("certs:issue") issues a certificate.
	PermissionCertsIssue = permission.CertsIssue
	// PermissionCertsEnroll ("certs:enroll") enrolls for a certificate.
	PermissionCertsEnroll = permission.CertsEnroll
	// PermissionCertsDelegate ("certs:delegate") delegates certificate
	// issuance.
	PermissionCertsDelegate = permission.CertsDelegate
	// PermissionCertsRevoke ("certs:revoke") revokes a certificate.
	PermissionCertsRevoke = permission.CertsRevoke
	// PermissionCertsList ("certs:list") lists certificates.
	PermissionCertsList = permission.CertsList
	// PermissionCertsRead ("certs:read") reads a certificate.
	PermissionCertsRead = permission.CertsRead
)

// Password vault permissions. qrdb stores no passwords: the consumer's
// password store enforces these.
const (
	// PermissionPasswordsRead ("passwords:read") reads stored passwords.
	PermissionPasswordsRead = permission.PasswordsRead
	// PermissionPasswordsWrite ("passwords:write") adds or updates stored
	// passwords.
	PermissionPasswordsWrite = permission.PasswordsWrite
	// PermissionPasswordsDelete ("passwords:delete") deletes stored
	// passwords.
	PermissionPasswordsDelete = permission.PasswordsDelete
	// PermissionPasswordsGenerate ("passwords:generate") generates passwords.
	PermissionPasswordsGenerate = permission.PasswordsGenerate
	// PermissionPasswordsLock ("passwords:lock") locks the password vault.
	PermissionPasswordsLock = permission.PasswordsLock
	// PermissionPasswordsUnlock ("passwords:unlock") unlocks the password
	// vault.
	PermissionPasswordsUnlock = permission.PasswordsUnlock
	// PermissionPasswordsOverrideOwnership ("passwords:override_ownership")
	// acts on a shared password whatever its owner.
	PermissionPasswordsOverrideOwnership = permission.PasswordsOverrideOwnership
)

// SSH key vault permissions.
const (
	// PermissionSSHRead ("ssh:read") reads SSH keys and host entries.
	PermissionSSHRead = permission.SSHRead
	// PermissionSSHWrite ("ssh:write") adds or updates SSH keys and host
	// entries.
	PermissionSSHWrite = permission.SSHWrite
	// PermissionSSHDelete ("ssh:delete") deletes SSH keys and host entries.
	PermissionSSHDelete = permission.SSHDelete
)

// PKCS#11 PIN and PIV slot permissions.
const (
	// PermissionPINRead ("pin:read") reads PIN state.
	PermissionPINRead = permission.PINRead
	// PermissionPINWrite ("pin:write") sets or changes a PIN.
	PermissionPINWrite = permission.PINWrite
	// PermissionPIVRead ("piv:read") reads PIV slots.
	PermissionPIVRead = permission.PIVRead
	// PermissionPIVWrite ("piv:write") writes PIV slots.
	PermissionPIVWrite = permission.PIVWrite
	// PermissionPIVUse ("piv:use") uses a PIV slot's key.
	PermissionPIVUse = permission.PIVUse
	// PermissionPIVDelete ("piv:delete") deletes a PIV slot.
	PermissionPIVDelete = permission.PIVDelete
)

// Device permissions, in the caller's own tenant.
const (
	// PermissionDevicesOnboard ("devices:onboard") onboards a device.
	PermissionDevicesOnboard = permission.DevicesOnboard
	// PermissionDevicePair ("device:pair") pairs a device to a user or
	// account.
	PermissionDevicePair = permission.DevicePair
	// PermissionDeviceRevoke ("device:revoke") revokes a paired device.
	PermissionDeviceRevoke = permission.DeviceRevoke
	// PermissionDeviceShare ("device:share") shares a device's access with
	// another identity in the tenant.
	PermissionDeviceShare = permission.DeviceShare
	// PermissionDeviceSync ("device:sync") syncs state across a user's
	// paired devices.
	PermissionDeviceSync = permission.DeviceSync
)
