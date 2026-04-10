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

package main

import (
	"errors"
	"strconv"
)

// errNoCertsInPEM is returned when a PEM file contains no valid
// certificate blocks.
var errNoCertsInPEM = errors.New("no certificates found in PEM data")

// PeerParseError is returned when a peer specification string cannot be
// parsed. The expected format is "id=address".
type PeerParseError struct {
	Input string
}

// Error returns a human-readable description of the parse failure.
func (e *PeerParseError) Error() string {
	return "cli: invalid peer format " + strconv.Quote(e.Input) + ", expected id=address"
}

// UnknownSMError is returned when the --sm flag specifies an unknown
// state machine type.
type UnknownSMError struct {
	Name string
}

// Error returns a human-readable description.
func (e *UnknownSMError) Error() string {
	return "cli: unknown state machine type " + strconv.Quote(e.Name)
}

// UnknownDiscoveryError is returned when the --discovery flag specifies
// an unknown discovery method.
type UnknownDiscoveryError struct {
	Name string
}

// Error returns a human-readable description.
func (e *UnknownDiscoveryError) Error() string {
	return "cli: unknown discovery method " + strconv.Quote(e.Name)
}

// MissingFlagError is returned when a required flag is not set.
type MissingFlagError struct {
	Flag string
}

// Error returns a human-readable description.
func (e *MissingFlagError) Error() string {
	return "cli: required flag --" + e.Flag + " is not set"
}

// NoPeersError is returned when no peers are specified for static
// discovery.
type NoPeersError struct{}

// Error returns a human-readable description.
func (e *NoPeersError) Error() string {
	return "cli: no peers specified, use --peers flag with static discovery"
}

// AdminFetchError is returned when an HTTP request to the admin server
// fails.
type AdminFetchError struct {
	URL string
	Err error
}

// Error returns a human-readable description.
func (e *AdminFetchError) Error() string {
	return "cli: failed to fetch " + e.URL + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *AdminFetchError) Unwrap() error {
	return e.Err
}

// AdminResponseError is returned when the admin server returns a
// non-200 status code.
type AdminResponseError struct {
	URL        string
	StatusCode int
}

// Error returns a human-readable description.
func (e *AdminResponseError) Error() string {
	return "cli: admin server returned status " + strconv.Itoa(e.StatusCode) + " for " + e.URL
}

// ConfigApplyError is returned when a YAML configuration file cannot be
// applied to the start flags.
type ConfigApplyError struct {
	Field string
	Err   error
}

// Error returns a human-readable description.
func (e *ConfigApplyError) Error() string {
	return "cli: config apply failed for " + strconv.Quote(e.Field) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ConfigApplyError) Unwrap() error {
	return e.Err
}

// TLSGenerateError is returned when TLS certificate generation fails.
type TLSGenerateError struct {
	Err error
}

// Error returns a human-readable description.
func (e *TLSGenerateError) Error() string {
	return "cli: TLS certificate generation failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *TLSGenerateError) Unwrap() error {
	return e.Err
}

// TLSLoadError is returned when loading TLS certificate material from
// a file fails.
type TLSLoadError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *TLSLoadError) Error() string {
	return "cli: failed to load TLS file " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *TLSLoadError) Unwrap() error {
	return e.Err
}

// CACertWriteError is returned when writing the CA certificate to disk
// fails.
type CACertWriteError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *CACertWriteError) Error() string {
	return "cli: failed to write CA certificate to " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CACertWriteError) Unwrap() error {
	return e.Err
}

// AuthBootstrapError is returned when authentication infrastructure
// initialization fails during the bootstrap sequence.
type AuthBootstrapError struct {
	Step string
	Err  error
}

// Error returns a human-readable description of the bootstrap failure.
func (e *AuthBootstrapError) Error() string {
	return "cli: auth bootstrap failed at " + strconv.Quote(e.Step) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *AuthBootstrapError) Unwrap() error {
	return e.Err
}

// AdminCertError is returned when admin client certificate generation fails.
type AdminCertError struct {
	Err error
}

// Error returns a human-readable description.
func (e *AdminCertError) Error() string {
	return "cli: admin client certificate generation failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *AdminCertError) Unwrap() error {
	return e.Err
}

// AdminCertWriteError is returned when writing the admin client cert to
// disk fails.
type AdminCertWriteError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *AdminCertWriteError) Error() string {
	return "cli: failed to write admin certificate to " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *AdminCertWriteError) Unwrap() error {
	return e.Err
}

// BootstrapFetchError is returned when fetching the CA bundle from a
// bootstrap server fails.
type BootstrapFetchError struct {
	Method string
	Err    error
}

// Error returns a human-readable description.
func (e *BootstrapFetchError) Error() string {
	return "cli: bootstrap fetch failed via " + e.Method + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *BootstrapFetchError) Unwrap() error {
	return e.Err
}

// BootstrapError is returned when the overall bootstrap process fails.
type BootstrapError struct {
	Err error
}

// Error returns a human-readable description.
func (e *BootstrapError) Error() string {
	return "cli: TLS bootstrap failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *BootstrapError) Unwrap() error {
	return e.Err
}

// BootstrapServerError is returned when starting a bootstrap server
// fails.
type BootstrapServerError struct {
	Protocol string
	Err      error
}

// Error returns a human-readable description.
func (e *BootstrapServerError) Error() string {
	return "cli: bootstrap server (" + e.Protocol + ") failed to start: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *BootstrapServerError) Unwrap() error {
	return e.Err
}

// BootstrapWriteError is returned when writing bootstrap material to
// disk fails.
type BootstrapWriteError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *BootstrapWriteError) Error() string {
	return "cli: failed to write bootstrap file " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *BootstrapWriteError) Unwrap() error {
	return e.Err
}

// BootstrapKeyError is returned when parsing bootstrap key material
// fails.
type BootstrapKeyError struct {
	Kind string
	Err  error
}

// Error returns a human-readable description.
func (e *BootstrapKeyError) Error() string {
	return "cli: bootstrap key parse failed (" + e.Kind + "): " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *BootstrapKeyError) Unwrap() error {
	return e.Err
}

// NoBootstrapAddrError is returned when no bootstrap address can be
// determined.
type NoBootstrapAddrError struct{}

// Error returns a human-readable description.
func (e *NoBootstrapAddrError) Error() string {
	return "cli: no bootstrap address available, use --bootstrap-addr or --bootstrap-noise-addr"
}

// CertReloadError is returned when hot-reloading a TLS certificate
// from disk fails.
type CertReloadError struct {
	Path string
	Err  error
}

// Error returns a human-readable description.
func (e *CertReloadError) Error() string {
	return "cli: TLS certificate reload failed for " + strconv.Quote(e.Path) + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CertReloadError) Unwrap() error {
	return e.Err
}

// SealStorageError is returned when the seal storage backend
// fails to initialize.
type SealStorageError struct {
	Err error
}

// Error returns a human-readable description.
func (e *SealStorageError) Error() string {
	return "cli: seal storage initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *SealStorageError) Unwrap() error {
	return e.Err
}

// SealStrategyError is returned when a sealing strategy fails to
// initialize.
type SealStrategyError struct {
	Strategy string
	Err      error
}

// Error returns a human-readable description.
func (e *SealStrategyError) Error() string {
	return "cli: seal strategy " + strconv.Quote(e.Strategy) + " initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *SealStrategyError) Unwrap() error {
	return e.Err
}

// SealBarrierError is returned when the storage barrier fails to
// initialize.
type SealBarrierError struct {
	Err error
}

// Error returns a human-readable description.
func (e *SealBarrierError) Error() string {
	return "cli: seal barrier initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *SealBarrierError) Unwrap() error {
	return e.Err
}

// CredentialServiceError is returned when the credential service
// fails to initialize.
type CredentialServiceError struct {
	Err error
}

// Error returns a human-readable description.
func (e *CredentialServiceError) Error() string {
	return "cli: credential service initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CredentialServiceError) Unwrap() error {
	return e.Err
}

// CeremonyServiceError is returned when the ceremony service fails
// to initialize.
type CeremonyServiceError struct {
	Err error
}

// Error returns a human-readable description.
func (e *CeremonyServiceError) Error() string {
	return "cli: ceremony service initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *CeremonyServiceError) Unwrap() error {
	return e.Err
}

// VectorStorageError is returned when the vector storage backend
// fails to initialize.
type VectorStorageError struct {
	Err error
}

// Error returns a human-readable description.
func (e *VectorStorageError) Error() string {
	return "cli: vector storage initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *VectorStorageError) Unwrap() error {
	return e.Err
}

// VectorServiceError is returned when the vector service fails to
// initialize.
type VectorServiceError struct {
	Err error
}

// Error returns a human-readable description.
func (e *VectorServiceError) Error() string {
	return "cli: vector service initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *VectorServiceError) Unwrap() error {
	return e.Err
}

// BarrierRegistryError is returned when the barrier registry fails to
// initialize. This prevents per-tenant encryption isolation from
// being established.
type BarrierRegistryError struct {
	Err error
}

// Error returns a human-readable description.
func (e *BarrierRegistryError) Error() string {
	return "cli: barrier registry initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *BarrierRegistryError) Unwrap() error {
	return e.Err
}

// ClusterDEKError is returned when the cluster DEK manager fails to
// initialize. This prevents the tenant DEK hierarchy from being
// established.
type ClusterDEKError struct {
	Err error
}

// Error returns a human-readable description.
func (e *ClusterDEKError) Error() string {
	return "cli: cluster DEK manager initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ClusterDEKError) Unwrap() error {
	return e.Err
}

// DEKStorageError is returned when the DEK storage backend fails to
// initialize.
type DEKStorageError struct {
	Err error
}

// Error returns a human-readable description.
func (e *DEKStorageError) Error() string {
	return "cli: DEK storage initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *DEKStorageError) Unwrap() error {
	return e.Err
}

// TenantDEKError is returned when the tenant DEK manager fails to
// initialize.
type TenantDEKError struct {
	Err error
}

// Error returns a human-readable description.
func (e *TenantDEKError) Error() string {
	return "cli: tenant DEK manager initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *TenantDEKError) Unwrap() error {
	return e.Err
}

// TenantServiceError is returned when the tenant lifecycle service fails
// to initialize.
type TenantServiceError struct {
	Err error
}

// Error returns a human-readable description.
func (e *TenantServiceError) Error() string {
	return "cli: tenant service initialization failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *TenantServiceError) Unwrap() error {
	return e.Err
}
