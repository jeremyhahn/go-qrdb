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

// The named-credential permissions are self-scope: every tenant has its own
// named-credential store, and an identity exercises these in its own tenant,
// against that tenant's store only. The system tenant's store is the node-wide
// one.
const (
	// PermissionCredentialsSubmit ("credentials:submit") submits a named
	// credential.
	PermissionCredentialsSubmit = permission.CredentialsSubmit

	// PermissionCredentialsStrategy ("credentials:strategy") reads the
	// strategy of the caller's credential store.
	PermissionCredentialsStrategy = permission.CredentialsStrategy

	// PermissionCredentialsList ("credentials:list") lists named credentials.
	PermissionCredentialsList = permission.CredentialsList

	// PermissionCredentialsDelete ("credentials:delete") deletes a named
	// credential.
	PermissionCredentialsDelete = permission.CredentialsDelete

	// PermissionCredentialsRotate ("credentials:rotate") rotates a named
	// credential to a new version.
	PermissionCredentialsRotate = permission.CredentialsRotate

	// PermissionCredentialsGCExpired ("credentials:gc_expired") removes
	// expired credential versions.
	PermissionCredentialsGCExpired = permission.CredentialsGCExpired

	// PermissionCredentialsRetrieveVersion ("credentials:retrieve_version")
	// retrieves one version of a named credential.
	PermissionCredentialsRetrieveVersion = permission.CredentialsRetrieveVersion

	// PermissionCredentialsListVersions ("credentials:list_versions") lists a
	// named credential's versions.
	PermissionCredentialsListVersions = permission.CredentialsListVersions

	// PermissionCredentialsStore ("credentials:store") stores a named
	// credential.
	PermissionCredentialsStore = permission.CredentialsStore

	// PermissionCredentialsRetrieve ("credentials:retrieve") retrieves a named
	// credential's current version.
	PermissionCredentialsRetrieve = permission.CredentialsRetrieve
)
