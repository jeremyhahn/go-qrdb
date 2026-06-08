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

package transport

// ---------------------------------------------------------------------------
// Phase 3 client providers
// ---------------------------------------------------------------------------
//
// The Phase 3 auth/identity clients (Permissions, RBAC, Factors, Login,
// Sessions, VolumePools) carry several method names that overlap with
// existing data-plane methods (List, Lookup, Verify, Pick, Remove,
// Revoke). To keep the embedded transport's flat method set unambiguous
// and to give the SDK a stable hand-off point, each phase-3 client is
// exposed via a provider accessor on the transport:
//
//	type PermissionProvider interface { PermissionClient() PermissionClient }
//	type RBACProvider       interface { RBACClient()       RBACClient }
//	type FactorProvider     interface { FactorClient()     FactorClient }
//	type LoginProvider      interface { LoginClient()      LoginClient }
//	type SessionProvider    interface { SessionClient()    SessionClient }
//	type VolumePoolProvider interface { VolumePoolClient() VolumePoolClient }
//
// SDK accessors type-assert the underlying transport against these
// providers; transports that don't expose a given client return nil
// from the SDK accessor. The embedded transport implements every
// provider; non-embedded transports (rest, grpc, mcp, quic, unix)
// return notImpl wrappers in Phase 3 and gain real implementations in
// Phase 4.

// PermissionProvider yields a PermissionClient. The embedded transport
// implements this; non-embedded transports return a notImpl wrapper
// until Phase 4 lands.
type PermissionProvider interface {
	PermissionClient() PermissionClient
}

// RBACProvider yields an RBACClient. The embedded transport implements
// this; non-embedded transports return a notImpl wrapper until Phase 4
// lands.
type RBACProvider interface {
	RBACClient() RBACClient
}

// FactorProvider yields a FactorClient. The embedded transport
// implements this; non-embedded transports return a notImpl wrapper
// until Phase 4 lands.
type FactorProvider interface {
	FactorClient() FactorClient
}

// LoginProvider yields a LoginClient. The embedded transport implements
// this; non-embedded transports return a notImpl wrapper until Phase 4
// lands.
type LoginProvider interface {
	LoginClient() LoginClient
}

// SessionProvider yields a SessionClient. The embedded transport
// implements this; non-embedded transports return a notImpl wrapper
// until Phase 4 lands.
type SessionProvider interface {
	SessionClient() SessionClient
}

// VolumePoolProvider yields a VolumePoolClient. The embedded transport
// implements this; non-embedded transports return a notImpl wrapper
// until Phase 4 lands.
type VolumePoolProvider interface {
	VolumePoolClient() VolumePoolClient
}

// AuditProvider yields an AuditClient that can emit and query
// compliance-grade audit events. The embedded transport implements this
// when the node injects its AuditLogger; transports without an audit
// surface (or with no logger configured) cause the SDK accessor to
// return nil.
type AuditProvider interface {
	AuditClient() AuditClient
}
