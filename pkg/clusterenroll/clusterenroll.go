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

// Package clusterenroll is the shared cluster-TLS enrollment orchestration
// for the 5-path TLS resolver, Noise NK bootstrap server, and truststrap join.
// It is built on pkg/clustertls and is consumed by cmd/qrdbd and by go-xkms
// via sdk/go/clusterenroll.
//
// The five resolution paths are:
//
//  1. Explicit files: TLSCAFile, TLSCertFile, TLSKeyFile all set.
//  2. Cached certs: data-dir already contains ca.pem, node-cert.pem, node-key.pem.
//  3. Single node: opts.SingleNode true — generates a self-signed CA.
//  4. Joining node: Bootstrap.HasFlags() true — fetches the CA bundle via truststrap.
//  5. First node: multi-node cluster without bootstrap flags — generates CA and
//     starts the Noise NK advertiser for peer enrollment.
package clusterenroll

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeremyhahn/go-truststrap/pkg/noiseproto"
	noiseboot "github.com/jeremyhahn/go-truststrap/pkg/noiseproto/bootstrap"
	"github.com/jeremyhahn/go-truststrap/pkg/truststrap"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
)

const (
	// DefaultNoisePort is the default TCP port for the Noise NK bootstrap
	// server used for peer enrollment.
	DefaultNoisePort = 63006

	// DefaultTimeout is the per-method timeout for bootstrap fetch operations.
	DefaultTimeout = 15 * time.Second

	// NoiseKeyFile is the filename used to persist the Noise static key in
	// the data directory.
	NoiseKeyFile = "noise-key.hex"
)

// readFileFn is os.ReadFile, indirected so a test can inject a read failure
// on a file that fileExists has already accepted. A mode-0000 file is not a
// substitute: CI job containers run as root, and root reads it successfully
// (conventions/testing-guidelines.md, Fault Injection).
var readFileFn = os.ReadFile

// Options holds the parameters for cluster-TLS enrollment orchestration.
// Callers populate this from their own configuration (e.g., CLI flags or
// YAML config) before calling Resolve.
type Options struct {
	// RaftAddress is the Raft node address, used as the SPIFFE node identity.
	RaftAddress string

	// TrustDomain is the cluster-wide SPIFFE trust domain
	// (e.g., "qrdb.cluster"). It is encoded into auto-generated certificates
	// as a URI SAN: spiffe://<TrustDomain>/node/<RaftAddress>.
	TrustDomain string

	// DataDir is the data directory for caching and persisting TLS material.
	DataDir string

	// SingleNode indicates this is a single-node cluster. The caller computes
	// this (e.g., via cluster.IsSingleNode). When true, path 3 is selected
	// instead of path 5.
	SingleNode bool

	// TLSCAFile, TLSCertFile, TLSKeyFile are explicit PEM file paths.
	// When all three are non-empty, path 1 (explicit files) is selected.
	TLSCAFile   string
	TLSCertFile string
	TLSKeyFile  string

	// Bootstrap holds options for joining an existing cluster via truststrap
	// (path 4). HasFlags() determines whether path 4 is selected.
	Bootstrap BootstrapOptions

	// Advertise holds options for the Noise NK bootstrap server started on
	// the first node of a multi-node cluster (path 5).
	Advertise AdvertiseOptions
}

// BootstrapOptions configures the truststrap join methods used when this node
// is joining an existing cluster (path 4).
type BootstrapOptions struct {
	// NoiseKey is the hex Curve25519 public key of the Noise bootstrap server.
	NoiseKey string

	// NoiseAddr is the host:port of the Noise bootstrap server.
	NoiseAddr string

	// SPKIPin is the hex SHA-256 SPKI pin for SPKI-pinned TLS bootstrap.
	SPKIPin string

	// ServerAddr is the host:port of the first node's admin server, used for
	// SPKI and DANE bootstrap methods.
	ServerAddr string

	// DANEDNS is the DNS server address for DANE TLSA lookup
	// (e.g., "8.8.8.8:53").
	DANEDNS string

	// DANEDoT enables DNS-over-TLS for the DANE resolver.
	DANEDoT bool

	// DirectURL is the full HTTPS URL for the direct bootstrap method.
	DirectURL string

	// DiscoveryMethod is the cluster discovery method (e.g., "static", "dns").
	// Used by ResolveServerURL to derive the bootstrap server URL.
	DiscoveryMethod string

	// Peers is the list of static peer specifications (format: "id=host:port").
	// Used by ResolveServerURL when DiscoveryMethod is "static".
	Peers []string

	// DNSDomain is the DNS domain used by ResolveServerURL when
	// DiscoveryMethod is "dns".
	DNSDomain string

	// AdminPort is the admin server port, used by ResolveServerURL to derive
	// the bootstrap HTTPS URL from a peer address or DNS domain. Zero is an
	// absent value, not a default: ResolveServerURL returns
	// *MissingAdminPortError rather than dialing a guessed port.
	AdminPort int
}

// HasFlags returns true when at least one bootstrap method is configured,
// indicating this node should join an existing cluster (path 4).
func (b BootstrapOptions) HasFlags() bool {
	return b.NoiseKey != "" ||
		b.NoiseAddr != "" ||
		b.SPKIPin != "" ||
		b.DANEDNS != "" ||
		b.DirectURL != ""
}

// AdvertiseOptions configures the Noise NK bootstrap server started on the
// first node of a multi-node cluster (path 5).
type AdvertiseOptions struct {
	// NoisePort is the TCP port for the Noise bootstrap server.
	// Zero defaults to DefaultNoisePort.
	NoisePort int

	// NoiseHost is the bind host for the Noise bootstrap server. It is
	// required: the enrollment listener is the one socket a joining node
	// reaches before it holds any credential, so this package will not
	// pick a bind host on the caller's behalf. The daemon sources it from
	// bootstrap.noise_host, falling back to server.host (go-qrdb#167).
	NoiseHost string

	// AdminPort is logged alongside the Noise public key so operators know
	// which port to configure on joining nodes. Zero means unconfigured
	// and is reported as such rather than substituted.
	AdminPort int
}

// Advertiser holds the running Noise NK bootstrap server started on the first
// node for peer enrollment. The Noise NK server allows joining nodes to
// securely fetch the cluster CA bundle using only the server's Curve25519
// public key as the trust anchor.
type Advertiser struct {
	noiseSrv *noiseboot.Server
	noiseKey *noiseproto.StaticKey

	// stopOnce makes Stop idempotent. It has to be: the daemon now defers a
	// Stop as soon as resolveTLS hands back a live advertiser AND still stops
	// it explicitly on the signal-shutdown path, so the ordinary run calls it
	// twice. Stop destroys the Noise static key's protected pages, and
	// destroying them twice is not a no-op in the general case.
	stopOnce sync.Once
}

// Stop gracefully shuts down the bootstrap server and releases the static
// key's protected pages. It is safe to call on a nil receiver, on a
// zero-value Advertiser (where the internal server was never started), and
// MORE THAN ONCE.
//
// The key is destroyed only after the server has stopped: the running server
// holds a noise.DHKey aliasing the protected region, and reading it after
// Destroy would touch freed pages. For the same reason the destroy runs once
// and only once, whichever caller gets there first.
func (a *Advertiser) Stop() {
	if a == nil || a.noiseSrv == nil {
		return
	}
	a.stopOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.noiseSrv.Stop(ctx)
		a.noiseKey.Destroy()
	})
}

// NoisePublicKey returns the Curve25519 public key as raw bytes. Returns nil
// when the Advertiser is nil or was not initialised with a Noise key.
func (a *Advertiser) NoisePublicKey() []byte {
	if a == nil || a.noiseKey == nil {
		return nil
	}
	return a.noiseKey.Public
}

// Addr returns the listen address of the Noise server, as bound (the
// configured NoiseHost joined with NoisePort, e.g. "10.0.0.1:63006").
// Returns an empty string when the Advertiser is nil or the server is not
// running.
func (a *Advertiser) Addr() string {
	if a == nil || a.noiseSrv == nil || a.noiseSrv.Addr() == nil {
		return ""
	}
	return a.noiseSrv.Addr().String()
}

// FetchError is returned when fetching the CA bundle from a bootstrap server
// fails (truststrap AutoFetch).
type FetchError struct {
	// Method identifies the bootstrap method that was attempted (e.g., "auto").
	Method string
	// Err is the underlying fetch error.
	Err error
}

// Error returns a human-readable description.
func (e *FetchError) Error() string {
	return "clusterenroll: bootstrap fetch failed via " + e.Method + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *FetchError) Unwrap() error { return e.Err }

// JoinError is returned when the overall bootstrap join process fails after a
// successful CA-bundle fetch (e.g., leaf certificate generation or persist).
type JoinError struct {
	// Err is the underlying error.
	Err error
}

// Error returns a human-readable description.
func (e *JoinError) Error() string {
	return "clusterenroll: TLS bootstrap failed: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *JoinError) Unwrap() error { return e.Err }

// ServerError is returned when starting or operating a bootstrap server fails.
type ServerError struct {
	// Protocol identifies the failed protocol (e.g., "noise").
	Protocol string
	// Err is the underlying error.
	Err error
}

// Error returns a human-readable description.
func (e *ServerError) Error() string {
	return "clusterenroll: bootstrap server (" + e.Protocol + ") failed to start: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *ServerError) Unwrap() error { return e.Err }

// ExplicitFilesError is returned by ResolveCA when TLS*File options are set.
// ResolveCA resolves only the cluster CA signing key; explicit TLS file options
// supply a pre-issued node cert/key pair rather than a CA key, making them
// incompatible with the CA-only resolver.
type ExplicitFilesError struct{}

// Error returns a human-readable description.
func (e *ExplicitFilesError) Error() string {
	return "clusterenroll: ResolveCA does not support explicit TLS file options; " +
		"clear TLSCAFile, TLSCertFile, and TLSKeyFile"
}

// validate checks the advertiser configuration.
//
// VALIDATION GOES ABOVE THE FIRST IRREVERSIBLE STEP, NEVER BELOW IT.
// Everything that can reject the request runs before anything is written,
// generated or handed out. Once a file has been persisted you are no longer
// validating, you are cleaning up, and cleanup on an error path is its own
// failure mode.
//
// Concretely here: Resolve and ResolveCA generate and persist cluster CA
// material before they start the advertiser. A check that lived only inside
// startNoiseAdvertiser would reject the FIRST attempt loudly and leave that
// material on disk -- and the next boot takes the cached-material path, which
// returns no advertiser at all. The operator would correct the configuration,
// see the node start cleanly, and have a first node that no peer can ever
// join. So both callers run this before generating anything.
func (o AdvertiseOptions) validate() error {
	if o.NoiseHost == "" {
		return &MissingBindHostError{Component: "noise bootstrap server"}
	}
	return nil
}

// MissingBindHostError is returned when a listener this package starts has no
// configured bind host. The bind host is a containment decision and belongs
// to the configuration layer; this package refuses to invent one.
type MissingBindHostError struct {
	// Component names the listener that has no bind host.
	Component string
}

// Error returns a human-readable description.
func (e *MissingBindHostError) Error() string {
	return "clusterenroll: no bind host configured for the " + e.Component +
		"; set bootstrap.noise_host (or server.host, which it defaults to)"
}

// MissingAdminPortError is returned when the bootstrap server URL must be
// built from a host plus the admin port and no admin port is configured.
type MissingAdminPortError struct {
	// Source names the address source the host came from.
	Source string
}

// Error returns a human-readable description.
func (e *MissingAdminPortError) Error() string {
	return "clusterenroll: cannot derive the bootstrap server URL from " + e.Source +
		": no admin port configured; set server.admin_port (or --admin-port)"
}

// clusterBundleProvider implements noiseboot.BundleProvider by concatenating
// the cluster CA certificate and CA private key as a single PEM bundle.
// Joining nodes receive this bundle over the Noise NK channel and use it to
// generate their own leaf certificates, sharing the cluster CA without the
// CA key ever leaving the first node in plaintext.
type clusterBundleProvider struct {
	caCertPEM []byte
	caKeyPEM  []byte
}

// CABundle returns the CA certificate and key concatenated as PEM bytes.
func (p *clusterBundleProvider) CABundle() ([]byte, error) {
	bundle := make([]byte, 0, len(p.caCertPEM)+len(p.caKeyPEM))
	bundle = append(bundle, p.caCertPEM...)
	bundle = append(bundle, p.caKeyPEM...)
	return bundle, nil
}

// Resolve implements the 5-path cluster-TLS enrollment strategy and returns
// the raw PEM material needed for mutual TLS.
//
// The returned Advertiser is non-nil for a node whose recorded enrollment role
// is RoleFirstNode -- both on the boot that generates the CA (path 5) and on
// every later boot, which loads the CA from cache (path 2). It used to be
// non-nil for path 5 alone, so a first node served enrollment on exactly one
// boot in its life and came up silently unjoinable on every boot after that
// (go-qrdb#241). It must be stopped during graceful shutdown.
func Resolve(ctx context.Context, opts *Options, logger *slog.Logger) (*clustertls.Material, *Advertiser, error) {
	// Path 1: explicit TLS files provided by the operator.
	if opts.TLSCAFile != "" && opts.TLSCertFile != "" && opts.TLSKeyFile != "" {
		logger.Info("loading TLS certificates from explicit paths",
			"ca", opts.TLSCAFile,
			"cert", opts.TLSCertFile,
			"key", opts.TLSKeyFile,
		)
		mat, err := clustertls.LoadFromFiles(opts.TLSCAFile, opts.TLSCertFile, opts.TLSKeyFile)
		if err != nil {
			return nil, nil, err
		}
		return mat, nil, nil
	}

	// Path 2: cached certificates already present in the data directory.
	mat, err := clustertls.LoadFromDir(opts.DataDir)
	if err != nil {
		return nil, nil, err
	}
	if mat != nil {
		logger.Info("loaded existing TLS certificates", "dir", opts.DataDir)
		// The cached path is reached on every boot after the first, so it is
		// where a first node's enrollment listener has to come back
		// (go-qrdb#241). Returning nil here unconditionally is what made a
		// first node silently unjoinable from its second boot onward.
		adv, advErr := ResumeAdvertiser(opts, mat, logger)
		if advErr != nil {
			return nil, nil, advErr
		}
		return mat, adv, nil
	}

	// Path 3: single-node cluster generates its own CA and node certificate.
	if opts.SingleNode {
		logger.Info("single-node mode, generating self-signed CA and node certificate")
		nodeURI := spiffe.NodeURI(opts.TrustDomain, opts.RaftAddress)
		mat, err := clustertls.GenerateCABundle(nodeURI)
		if err != nil {
			return nil, nil, err
		}
		if err := persistRole(opts.DataDir, RoleSingleNode); err != nil {
			return nil, nil, err
		}
		if err := clustertls.PersistMaterial(mat, opts.DataDir); err != nil {
			return nil, nil, err
		}
		return mat, nil, nil
	}

	// Path 4: joining node fetches the CA bundle from the existing cluster.
	if opts.Bootstrap.HasFlags() {
		logger.Info("bootstrap flags detected, fetching CA bundle from existing cluster")
		if err := persistRole(opts.DataDir, RoleJoined); err != nil {
			return nil, nil, err
		}
		mat, err := Join(ctx, opts, logger)
		if err != nil {
			return nil, nil, err
		}
		return mat, nil, nil
	}

	// Path 5: first node of a multi-node cluster generates the CA, persists
	// it, and starts the Noise NK advertiser for peer enrollment.
	//
	// The advertiser configuration is validated BEFORE any material is
	// generated. Persisting a CA and then failing to start the advertiser
	// would leave this node on path 2 (cached certificates) for every
	// subsequent boot, and path 2 returns no advertiser -- so the node would
	// come up looking healthy while no peer could ever enroll, and correcting
	// the configuration would not undo it.
	if err := opts.Advertise.validate(); err != nil {
		return nil, nil, err
	}
	logger.Info("first node of multi-node cluster, generating CA and starting bootstrap servers")
	nodeURI := spiffe.NodeURI(opts.TrustDomain, opts.RaftAddress)
	mat, err = clustertls.GenerateCABundle(nodeURI)
	if err != nil {
		return nil, nil, err
	}
	// Recorded BEFORE the material: see persistRole. It is what lets every
	// later boot resume this node's listener instead of coming up quiet.
	if err := persistRole(opts.DataDir, RoleFirstNode); err != nil {
		return nil, nil, err
	}
	if err := clustertls.PersistMaterial(mat, opts.DataDir); err != nil {
		return nil, nil, err
	}
	adv, err := StartAdvertiser(mat, opts.Advertise, opts.DataDir, logger)
	if err != nil {
		return nil, nil, err
	}
	logAdvertiserInfo(adv, opts.Advertise)
	return mat, adv, nil
}

// ResolveCA resolves the cluster CA material (cert + key PEM) WITHOUT
// generating a node leaf — for consumers that supply their own node key (e.g.
// hardware-resident) and issue their own leaf via clustertls.IssueLeafForKey.
// It mirrors Resolve's path logic minus leaf generation:
//   - explicit files: not supported (return ExplicitFilesError).
//   - cached:         clustertls.LoadCAFromDir(opts.DataDir).
//   - single/first:   clustertls.GenerateCA → PersistCA. The first node
//     (!SingleNode) also starts the Noise Advertiser, as Resolve path 5 does.
//   - join:           fetch via truststrap AutoFetch, split bundle into
//     caCertPEM+caKeyPEM, PersistCA.
//
// The returned Advertiser is non-nil for a node whose recorded enrollment role
// is RoleFirstNode, on the boot that generates the CA and on every later boot
// that loads it from cache (go-qrdb#241), and must be stopped during graceful
// shutdown. Returns (nil, nil, nil, ExplicitFilesError) when any TLS*File
// option is set.
func ResolveCA(ctx context.Context, opts *Options, logger *slog.Logger) (caCertPEM, caKeyPEM []byte, adv *Advertiser, err error) {
	// Explicit TLS file mode is not supported: those paths supply a node
	// cert/key pair, not a CA signing key.
	if opts.TLSCAFile != "" || opts.TLSCertFile != "" || opts.TLSKeyFile != "" {
		return nil, nil, nil, &ExplicitFilesError{}
	}

	// Cached: load CA from dataDir when both ca.pem and ca-key.pem exist.
	caCertPEM, caKeyPEM, err = clustertls.LoadCAFromDir(opts.DataDir)
	if err != nil {
		return nil, nil, nil, err
	}
	if caCertPEM != nil && caKeyPEM != nil {
		logger.Info("loaded existing CA from cache", "dir", opts.DataDir)
		// Every boot after the first lands here, so this is where a first
		// node's enrollment listener has to come back (go-qrdb#241).
		adv, advErr := ResumeAdvertiser(opts, &clustertls.Material{
			CACertPEM: caCertPEM,
			CAKeyPEM:  caKeyPEM,
		}, logger)
		if advErr != nil {
			return nil, nil, nil, advErr
		}
		return caCertPEM, caKeyPEM, adv, nil
	}

	// Join: fetch CA bundle from an existing cluster node via truststrap.
	if opts.Bootstrap.HasFlags() {
		logger.Info("bootstrap flags detected, fetching CA bundle from existing cluster")
		bundlePEM, fetchErr := autoFetchBundle(ctx, opts, logger)
		if fetchErr != nil {
			return nil, nil, nil, fetchErr
		}
		caCertPEM, caKeyPEM, err = splitCABundle(bundlePEM)
		if err != nil {
			return nil, nil, nil, &JoinError{Err: err}
		}
		if err := persistRole(opts.DataDir, RoleJoined); err != nil {
			return nil, nil, nil, err
		}
		if err := clustertls.PersistCA(caCertPEM, caKeyPEM, opts.DataDir); err != nil {
			return nil, nil, nil, err
		}
		return caCertPEM, caKeyPEM, nil, nil
	}

	// Single node or first node: generate a new CA and persist it. The
	// first-node path validates the advertiser configuration first, for the
	// reason given on Resolve path 5: persisted CA material sends every later
	// boot down the cached path, which starts no advertiser.
	if opts.SingleNode {
		logger.Info("single-node mode, generating self-signed CA")
	} else {
		if err := opts.Advertise.validate(); err != nil {
			return nil, nil, nil, err
		}
		logger.Info("first node of multi-node cluster, generating CA and starting bootstrap server")
	}
	caCertPEM, caKeyPEM, _, _, err = clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		return nil, nil, nil, err
	}
	// Recorded BEFORE the material: see persistRole.
	role := RoleFirstNode
	if opts.SingleNode {
		role = RoleSingleNode
	}
	if err := persistRole(opts.DataDir, role); err != nil {
		return nil, nil, nil, err
	}
	if err := clustertls.PersistCA(caCertPEM, caKeyPEM, opts.DataDir); err != nil {
		return nil, nil, nil, err
	}

	// First node of a multi-node cluster: start the Noise advertiser so that
	// joining nodes can call Join (or ResolveCA with bootstrap flags).
	if !opts.SingleNode {
		mat := &clustertls.Material{
			CACertPEM: caCertPEM,
			CAKeyPEM:  caKeyPEM,
		}
		adv, err = StartAdvertiser(mat, opts.Advertise, opts.DataDir, logger)
		if err != nil {
			return nil, nil, nil, err
		}
		logAdvertiserInfo(adv, opts.Advertise)
	}

	return caCertPEM, caKeyPEM, adv, nil
}

// Join fetches the cluster CA bundle from an existing node via truststrap,
// generates a leaf certificate signed by that CA, and persists everything to
// opts.DataDir. This is the path 4 implementation; it is also exported so that
// cmd/qrdbd can provide a thin bootstrapTLS wrapper that preserves the
// qrdbd-specific TLSBundle type.
func Join(ctx context.Context, opts *Options, logger *slog.Logger) (*clustertls.Material, error) {
	bundlePEM, err := autoFetchBundle(ctx, opts, logger)
	if err != nil {
		return nil, err
	}

	nodeURI := spiffe.NodeURI(opts.TrustDomain, opts.RaftAddress)
	mat, err := clustertls.GenerateLeafFromCA(bundlePEM, nodeURI)
	if err != nil {
		return nil, &JoinError{Err: err}
	}

	if err := clustertls.PersistMaterial(mat, opts.DataDir); err != nil {
		return nil, &JoinError{Err: err}
	}

	logger.Info("TLS bootstrap complete, certificates persisted", "dir", opts.DataDir)
	return mat, nil
}

// StartAdvertiser starts the Noise NK bootstrap server so that joining nodes
// can securely fetch the cluster CA bundle. The server's static key is
// persisted to {dataDir}/noise-key.hex for reuse across restarts; if the key
// file already exists it is loaded instead of generating a new one.
//
// This is the path 5 server-start implementation; it is also exported so that
// cmd/qrdbd can provide a thin startBootstrapServers wrapper that accepts the
// qrdbd-specific TLSBundle type.
func StartAdvertiser(mat *clustertls.Material, opts AdvertiseOptions, dataDir string, logger *slog.Logger) (*Advertiser, error) {
	provider := &clusterBundleProvider{
		caCertPEM: mat.CACertPEM,
		caKeyPEM:  mat.CAKeyPEM,
	}
	return startNoiseAdvertiser(provider, opts, dataDir, logger)
}

// startNoiseAdvertiser starts the Noise NK bootstrap server with the given
// bundle provider, persisting (or reusing) the static Noise key at
// {dataDir}/noise-key.hex. It is the shared implementation behind
// StartAdvertiser (CA cert+key bundle) and StartAnchorAdvertiser
// (anchor-only bundle).
func startNoiseAdvertiser(provider noiseboot.BundleProvider, opts AdvertiseOptions, dataDir string, logger *slog.Logger) (*Advertiser, error) {
	// Configuration is checked before anything with a side effect runs.
	// The bind host is never chosen here: a node contained to one interface
	// must not have its enrollment listener quietly opened on all of them,
	// and this package cannot know which containment the operator asked for
	// -- only the configuration layer can. Rejecting it up here also means a
	// refused configuration leaves no freshly generated Noise static key on
	// disk, which the key-generation branch below would otherwise persist
	// before the address was ever looked at.
	if err := opts.validate(); err != nil {
		return nil, err
	}

	keyPath := filepath.Join(dataDir, NoiseKeyFile)

	var noiseKey *noiseproto.StaticKey
	var err error

	if fileExists(keyPath) {
		hexData, readErr := readFileFn(keyPath)
		if readErr != nil {
			return nil, &ServerError{Protocol: "noise", Err: readErr}
		}
		// DecodeStaticKey writes the private scalar straight into protected
		// memory, so the hex never passes through an immutable Go string.
		// The file bytes are ours to wipe once decoded.
		trimmed := bytes.TrimSpace(hexData)
		noiseKey, err = noiseproto.DecodeStaticKey(trimmed)
		noiseproto.WipeBytes(hexData)
		if err != nil {
			return nil, &ServerError{Protocol: "noise", Err: err}
		}
		logger.Info("loaded existing Noise static key", "path", keyPath)
	} else {
		noiseKey, err = noiseproto.GenerateStaticKey()
		if err != nil {
			return nil, &ServerError{Protocol: "noise", Err: err}
		}
		// The hex encoding is as sensitive as the key itself: wipe the buffer
		// once it has reached the file.
		hexEncoded := make([]byte, noiseproto.HexKeySize)
		defer noiseproto.WipeBytes(hexEncoded)
		if err := noiseproto.EncodeStaticKey(hexEncoded, noiseKey); err != nil {
			noiseKey.Destroy()
			return nil, &ServerError{Protocol: "noise", Err: err}
		}
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			noiseKey.Destroy()
			return nil, &ServerError{Protocol: "noise", Err: err}
		}
		// Atomic, for the reason persistRole is: os.WriteFile truncates in
		// place, and a crash mid-write leaves a partial hex key that
		// DecodeStaticKey refuses on every later boot. Since go-qrdb#241 this
		// path runs on EVERY boot of a first node rather than only the one
		// that generated the CA, so a truncated key file now bricks every
		// start instead of one.
		if err := clustertls.WriteFileAtomic(keyPath, hexEncoded, 0o600); err != nil {
			noiseKey.Destroy()
			return nil, &ServerError{Protocol: "noise", Err: err}
		}
		logger.Info("generated and persisted Noise static key", "path", keyPath)
	}

	listenPort := opts.NoisePort
	if listenPort == 0 {
		listenPort = DefaultNoisePort
	}
	listenAddr := net.JoinHostPort(opts.NoiseHost, strconv.Itoa(listenPort))

	// The embedded DHKey aliases the protected private scalar; the Advertiser
	// owns the StaticKey from here and releases it in Stop.
	srv, err := noiseboot.NewServer(&noiseboot.ServerConfig{
		ListenAddr: listenAddr,
		StaticKey:  &noiseKey.DHKey,
		CABundler:  provider,
		Logger:     logger,
	})
	if err != nil {
		noiseKey.Destroy()
		return nil, &ServerError{Protocol: "noise", Err: err}
	}

	if err := srv.Start(); err != nil {
		noiseKey.Destroy()
		return nil, &ServerError{Protocol: "noise", Err: err}
	}

	logger.Info("Noise NK bootstrap server started", "addr", srv.Addr().String())

	return &Advertiser{
		noiseSrv: srv,
		noiseKey: noiseKey,
	}, nil
}

// ResolveServerURL derives the HTTPS URL of the bootstrap server from
// BootstrapOptions. It checks, in order: explicit ServerAddr, first static
// peer address (combined with AdminPort), and DNSDomain (combined with
// AdminPort). It returns an empty URL and a nil error when no address source
// is configured at all -- that is a caller stating it has no bootstrap
// server, not a failure.
//
// It fails closed with *MissingAdminPortError when an address source IS
// configured but AdminPort is zero. The previous behaviour substituted 8443,
// a literal that exists nowhere in the defaults layer and contradicts
// DefaultConfig().Server.AdminPort, so a caller that forgot to plumb the port
// silently dialed a port nothing in this system listens on (go-qrdb#167).
func ResolveServerURL(opts BootstrapOptions) (string, error) {
	// Explicit bootstrap address takes priority.
	if opts.ServerAddr != "" {
		return "https://" + opts.ServerAddr, nil
	}

	// Extract host from the first static peer and combine with admin port.
	if opts.DiscoveryMethod == "static" && len(opts.Peers) > 0 {
		if host := ExtractHostFromPeer(opts.Peers[0]); host != "" {
			if opts.AdminPort == 0 {
				return "", &MissingAdminPortError{Source: "static peer " + opts.Peers[0]}
			}
			return "https://" + net.JoinHostPort(host, strconv.Itoa(opts.AdminPort)), nil
		}
	}

	// Fall back to DNS domain with admin port.
	if opts.DNSDomain != "" {
		if opts.AdminPort == 0 {
			return "", &MissingAdminPortError{Source: "DNS domain " + opts.DNSDomain}
		}
		return "https://" + net.JoinHostPort(opts.DNSDomain, strconv.Itoa(opts.AdminPort)), nil
	}

	return "", nil
}

// ExtractHostFromPeer extracts the host portion from a peer specification in
// the format "id=host:port". Returns an empty string if the format is invalid.
func ExtractHostFromPeer(peer string) string {
	parts := strings.SplitN(peer, "=", 2)
	if len(parts) != 2 {
		return ""
	}
	addr := parts[1]
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		// Address without port; use as-is.
		return addr
	}
	return host
}

// autoFetchBundle builds a truststrap AutoConfig from opts and calls AutoFetch
// to retrieve the cluster CA bundle PEM from an existing cluster node.
// It is shared by Join and ResolveCA to avoid duplicating the truststrap
// configuration logic.
func autoFetchBundle(ctx context.Context, opts *Options, logger *slog.Logger) ([]byte, error) {
	cfg := &truststrap.AutoConfig{
		PerMethodTimeout: DefaultTimeout,
		Logger:           logger,
	}

	var methods []truststrap.Method

	// Configure Noise NK if both key and address are provided.
	if opts.Bootstrap.NoiseKey != "" && opts.Bootstrap.NoiseAddr != "" {
		cfg.Noise = &truststrap.NoiseConfig{
			ServerAddr:      opts.Bootstrap.NoiseAddr,
			ServerStaticKey: opts.Bootstrap.NoiseKey,
			Logger:          logger,
		}
		methods = append(methods, truststrap.MethodNoise)
	}

	// Configure SPKI-pinned TLS if pin and server address are provided.
	if opts.Bootstrap.SPKIPin != "" && opts.Bootstrap.ServerAddr != "" {
		cfg.SPKI = &truststrap.SPKIConfig{
			ServerURL:     "https://" + opts.Bootstrap.ServerAddr,
			SPKIPinSHA256: opts.Bootstrap.SPKIPin,
			Logger:        logger,
		}
		methods = append(methods, truststrap.MethodSPKI)
	}

	// Configure DANE if a DNS server is provided.
	if opts.Bootstrap.DANEDNS != "" {
		serverURL, urlErr := ResolveServerURL(opts.Bootstrap)
		if urlErr != nil {
			return nil, &FetchError{Method: "dane", Err: urlErr}
		}
		if serverURL != "" {
			cfg.DANE = &truststrap.DANEConfig{
				ServerURL:  serverURL,
				DNSServer:  opts.Bootstrap.DANEDNS,
				DNSOverTLS: opts.Bootstrap.DANEDoT,
				Logger:     logger,
			}
			methods = append(methods, truststrap.MethodDANE)
		}
	}

	// Configure Direct HTTPS if a URL is provided.
	if opts.Bootstrap.DirectURL != "" {
		cfg.Direct = &truststrap.DirectConfig{
			ServerURL: opts.Bootstrap.DirectURL,
			Logger:    logger,
		}
		methods = append(methods, truststrap.MethodDirect)
	}

	if len(methods) > 0 {
		cfg.MethodOrder = methods
	}

	fetchCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	resp, err := truststrap.AutoFetch(fetchCtx, cfg)
	if err != nil {
		return nil, &FetchError{Method: "auto", Err: err}
	}

	return resp.BundlePEM, nil
}

// splitCABundle splits a concatenated CA certificate + CA private key PEM
// bundle (as produced by clusterBundleProvider.CABundle) into the two
// PEM-encoded components. It mirrors the parsing logic in
// clustertls.GenerateLeafFromCA. Returns a *clustertls.KeyError when the
// expected PEM blocks are absent or malformed.
func splitCABundle(bundlePEM []byte) (caCertPEM, caKeyPEM []byte, err error) {
	caBlock, rest := pem.Decode(bundlePEM)
	if caBlock == nil || caBlock.Type != "CERTIFICATE" {
		return nil, nil, &clustertls.KeyError{
			Kind: "CA certificate",
			Err:  errors.New("no CERTIFICATE block found in PEM data"),
		}
	}
	keyBlock, _ := pem.Decode(rest)
	if keyBlock == nil || keyBlock.Type != "EC PRIVATE KEY" {
		return nil, nil, &clustertls.KeyError{
			Kind: "CA private key",
			Err:  errors.New("no EC PRIVATE KEY block found in PEM data"),
		}
	}
	return pem.EncodeToMemory(caBlock), pem.EncodeToMemory(keyBlock), nil
}

// logAdvertiserInfo logs the information operators need to configure joining
// nodes: the Noise public key (hex-encoded), the bootstrap server address, and
// the admin server port.
func logAdvertiserInfo(adv *Advertiser, opts AdvertiseOptions) {
	if adv == nil || adv.noiseKey == nil {
		return
	}
	// The admin port is reported as configured. Substituting a literal here
	// would hand operators a port to configure on joining nodes that this
	// node is not listening on (go-qrdb#167).
	if opts.AdminPort == 0 {
		slog.Warn("bootstrap enrollment: admin port is unconfigured; joining nodes cannot be told which port to use",
			"noise_public_key", hex.EncodeToString(adv.noiseKey.Public),
			"noise_addr", adv.Addr(),
		)
		return
	}
	slog.Info("bootstrap enrollment information for joining nodes",
		"noise_public_key", hex.EncodeToString(adv.noiseKey.Public),
		"noise_addr", adv.Addr(),
		"admin_port", opts.AdminPort,
	)
}

// fileExists returns true when path refers to an existing regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
