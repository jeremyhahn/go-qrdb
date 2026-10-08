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

package auth

import "strings"

// ServicePrincipalSubjectPrefix is the reserved Subject namespace of a
// node-sanctioned service principal: the identity a node grants an embedding
// host for the host's OWN in-process calls (loading its CA state, its stores,
// its signing keys). The principal named "xkms" has Subject
// "qrdb-service:xkms".
//
// No network transport ever accepts a Subject in this namespace: the
// authentication middleware refuses any presented identity that carries it,
// whatever credential produced it (see IsServicePrincipal).
const ServicePrincipalSubjectPrefix = "qrdb-service:"

// ServicePrincipalMetadataKey is the Identity.Metadata key NewServicePrincipal
// sets, to the principal's name. Its presence, with any value, marks the
// identity as a service principal (see IsServicePrincipal).
const ServicePrincipalMetadataKey = "qrdb_service_principal"

// NewServicePrincipal returns the identity of the service principal name in
// tenantID, holding exactly perms.
//
// The permissions travel as ScopePermissions: the authorizer unions them with
// the (empty) role set, so the principal holds what it was given and nothing
// else, and every other rule -- the SO deny-list, the permission's scope
// against the target tenant, ABAC -- applies to it unchanged. There is no
// role that makes it a superuser and no path that skips the authorizer.
//
// Only the node constructs one (sdk/go/node Node.ServiceContext), for calls
// made in its own process; never from anything a request carried.
func NewServicePrincipal(name, tenantID string, perms []string) *Identity {
	return &Identity{
		Subject:          ServicePrincipalSubjectPrefix + name,
		TenantID:         tenantID,
		ScopePermissions: append([]string(nil), perms...),
		Metadata:         map[string]string{ServicePrincipalMetadataKey: name},
	}
}

// IsServicePrincipal reports whether id is, or claims to be, a service
// principal: its Subject is in the reserved ServicePrincipalSubjectPrefix
// namespace or its Metadata carries ServicePrincipalMetadataKey. Either one is
// a claim, so either one is refused when it arrives over a network transport.
func IsServicePrincipal(id *Identity) bool {
	if id == nil {
		return false
	}
	if _, marked := id.Metadata[ServicePrincipalMetadataKey]; marked {
		return true
	}
	return strings.HasPrefix(id.Subject, ServicePrincipalSubjectPrefix)
}
