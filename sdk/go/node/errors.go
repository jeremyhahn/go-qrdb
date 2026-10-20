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

package node

import (
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// StorageCreateError indicates that the storage backend could not be created.
type StorageCreateError struct {
	Backend string
	Err     error
}

// Error returns a human-readable description.
func (e *StorageCreateError) Error() string {
	return "node: failed to create storage backend " + e.Backend + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *StorageCreateError) Unwrap() error { return e.Err }

// EngineCreateError indicates that the store engine could not be created.
type EngineCreateError struct {
	Engine string
	Err    error
}

// Error returns a human-readable description.
func (e *EngineCreateError) Error() string {
	return "node: failed to create engine " + e.Engine + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *EngineCreateError) Unwrap() error { return e.Err }

// ServicesCreateError indicates that the lifecycle service graph could not
// be constructed.
type ServicesCreateError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ServicesCreateError) Error() string {
	return "node: failed to build services: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ServicesCreateError) Unwrap() error { return e.Err }

// ServerCreateError indicates that the multi-protocol server could not be
// started.
type ServerCreateError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ServerCreateError) Error() string {
	return "node: failed to start server: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ServerCreateError) Unwrap() error { return e.Err }

// HostCreateError indicates that the quicraft host could not be created or
// the initial shard could not be started.
type HostCreateError struct {
	Err error
}

// Error returns a human-readable description.
func (e *HostCreateError) Error() string {
	return "node: failed to create clustered host: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *HostCreateError) Unwrap() error { return e.Err }

// ConfigError indicates an invalid node configuration was supplied.
type ConfigError struct {
	Field   string
	Message string
	// Err is the typed cause, when the refusal came from a lower layer.
	Err error
}

// Error returns a human-readable description.
func (e *ConfigError) Error() string {
	return "node: config error: " + e.Field + " " + e.Message
}

// Unwrap returns the typed cause, if any.
func (e *ConfigError) Unwrap() error { return e.Err }

// ClusterIdentityError indicates that a clustered node handed no cluster
// identity could not resolve its own: its certificate and cluster CA could be
// neither loaded from its data directory nor generated into it.
type ClusterIdentityError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ClusterIdentityError) Error() string {
	return "node: failed to resolve the cluster identity: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ClusterIdentityError) Unwrap() error { return e.Err }

// ClientCreateError indicates that the embedded SDK client could not be
// created.
type ClientCreateError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ClientCreateError) Error() string {
	return "node: failed to create SDK client: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ClientCreateError) Unwrap() error { return e.Err }

// UnsealError indicates that the seal barrier could not be unsealed during
// node construction or a subsequent Unseal call.
type UnsealError struct {
	Err error
}

// Error returns a human-readable description.
func (e *UnsealError) Error() string {
	return "node: failed to unseal barrier: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *UnsealError) Unwrap() error { return e.Err }

// ErrNoTenantRouting indicates the node lifecycle produced no tenant-routed
// user auth-factor service, so there is nothing to wire the unified-login
// ceremony onto.
//
// It is a refusal rather than a fallback. The routed service IS the tenant
// boundary of factor resolution (go-qrdb#393): a login surface built over the
// node's bare KV servicer resolves every tenant's credentials out of one
// keyspace, which is the defect, so a node that cannot route offers no login
// rather than an unrouted one.
var ErrNoTenantRouting = errors.New("node: no tenant-routed user auth-factor service")

// ErrNodeClosed is returned by Unseal and EnableEncryption on a node that
// Close has begun or finished tearing down. It lets a caller tell "this node
// is closed" apart from a credential or migration failure (go-qrdb#226). It is
// service.ErrNodeClosed itself, so a refusal delivered over a transport is
// classified as retryable (503 / Unavailable) there too.
var ErrNodeClosed = service.ErrNodeClosed

// LoginWiringError indicates that the unified-login ceremony could not be
// wired into the embedded transport — typically because the node lifecycle
// produced no tenant-routed UserAuthFactor service to back the LoginResolver.
type LoginWiringError struct {
	Err error
}

// Error returns a human-readable description.
func (e *LoginWiringError) Error() string {
	return "node: failed to wire unified-login ceremony: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *LoginWiringError) Unwrap() error { return e.Err }

// ErrNoAuthFactorTenantRouting indicates the node lifecycle produced no
// tenant-routed store resolver, so there is nothing to route the
// ceremony-time custodian AuthFactorService through.
//
// It is a refusal rather than a fallback, for the same reason
// ErrNoTenantRouting is: an AuthFactorService built over the node's bare KV
// servicer (svcNode.KV) resolves every tenant's custodian enrollments out of
// one keyspace, which is the defect (#464) closing this one for the
// user-side service (#393) left behind. In practice svcNode.StoreResolver is
// always non-nil — lifecycle.BuildServices constructs one whether or not a
// barrier is configured — so this is reached only by a lifecycle composition
// that has not wired a resolver at all.
var ErrNoAuthFactorTenantRouting = errors.New("node: no tenant-routed store resolver for the auth-factor service")

// AuthFactorWiringError indicates that the AuthFactorService could not be
// constructed from the caller-supplied FactorRegistry. This prevents a
// misconfigured enrollment/challenge/verify surface from silently
// degrading to "not supported" across the embedded transport and all
// wire protocol servers.
type AuthFactorWiringError struct {
	Err error
}

// Error returns a human-readable description.
func (e *AuthFactorWiringError) Error() string {
	return "node: failed to wire AuthFactorService from FactorRegistry: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *AuthFactorWiringError) Unwrap() error { return e.Err }

// PendingAdminCertWriteError is returned when wireClientAndServer could not
// record this node's issued-or-adopted bootstrap admin certificate into the
// durable pending-admin-cert hand-off (go-qrdb#548 review item 6):
// firstboot.WriteIssuedAdminCertIfAny failed for a reason other than
// another node already having won the create-if-absent race (which is
// tolerated, not an error). Without this record FirstBootWithCeremony on
// whichever node runs first boot has nothing to resolve the admin's
// certificate from.
type PendingAdminCertWriteError struct {
	Err error
}

// Error returns a human-readable description.
func (e *PendingAdminCertWriteError) Error() string {
	return "node: failed to record the pending admin certificate: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *PendingAdminCertWriteError) Unwrap() error { return e.Err }

// TenantInfraError indicates that the per-tenant isolation stack (built by
// server.BuildTenantInfra) could not be assembled or that a boot re-attach of
// an existing IsolationVolume tenant failed. This is only returned when
// WithTenantIsolation() is active.
type TenantInfraError struct {
	Err error
}

// Error returns a human-readable description.
func (e *TenantInfraError) Error() string {
	return "node: failed to build tenant infrastructure: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *TenantInfraError) Unwrap() error { return e.Err }

// ABACWiringError indicates that the ABAC authorization decorator could
// not be constructed from the configured policies or policy directory.
type ABACWiringError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ABACWiringError) Error() string {
	return "node: failed to wire ABAC authorization decorator: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ABACWiringError) Unwrap() error { return e.Err }

// ErrNilWrappedAuthorizer indicates that the embedder's authorizer wrapper
// (WithAuthorizerWrapper) returned nil instead of an auth.Authorizer. Node
// construction fails closed: a nil authorizer would leave every transport
// with no authorization decision at all.
var ErrNilWrappedAuthorizer = errors.New("node: authorizer wrapper returned nil")

// AuthMiddlewareWiringError indicates that the multi-protocol server's
// production auth-enforcement middleware (pkg/auth/wiring.BuildAuthMiddleware
// — the RBAC[+ABAC] authorizer, mTLS authenticator, and user-store
// enrichment shared with cmd/qrdbd) could not be constructed. This aborts
// node construction rather than silently starting the server unauthenticated:
// auth is enabled (cfg.Auth.Enabled) but a required dependency (the role or
// user service) was unavailable, or the configured ABAC policies failed to
// load/compile.
type AuthMiddlewareWiringError struct {
	Err error
}

// Error returns a human-readable description.
func (e *AuthMiddlewareWiringError) Error() string {
	return "node: failed to wire auth middleware for the multi-protocol server: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *AuthMiddlewareWiringError) Unwrap() error { return e.Err }
