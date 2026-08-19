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
	// the bootstrap HTTPS URL. Defaults to 8443 when zero.
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

	// AdminPort is logged alongside the Noise public key so operators know
	// which port to configure on joining nodes. Defaults to 8443 when zero.
	AdminPort int
}

// Advertiser holds the running Noise NK bootstrap server started on the first
// node for peer enrollment. The Noise NK server allows joining nodes to
// securely fetch the cluster CA bundle using only the server's Curve25519
// public key as the trust anchor.
type Advertiser struct {
	noiseSrv *noiseboot.Server
	noiseKey *noiseproto.StaticKey
}

// Stop gracefully shuts down the bootstrap server and releases the static
// key's protected pages. It is safe to call on a nil receiver and on a
// zero-value Advertiser (where the internal server was never started).
//
// The key is destroyed only after the server has stopped: the running server
// holds a noise.DHKey aliasing the protected region, and reading it after
// Destroy would touch freed pages.
func (a *Advertiser) Stop() {
	if a == nil || a.noiseSrv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = a.noiseSrv.Stop(ctx)
	a.noiseKey.Destroy()
}

// NoisePublicKey returns the Curve25519 public key as raw bytes. Returns nil
// when the Advertiser is nil or was not initialised with a Noise key.
func (a *Advertiser) NoisePublicKey() []byte {
	if a == nil || a.noiseKey == nil {
		return nil
	}
	return a.noiseKey.Public
}

// Addr returns the listen address of the Noise server (e.g., "0.0.0.0:63006").
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
// the raw PEM material needed for mutual TLS. The returned Advertiser is
// non-nil only for path 5 (first node of a multi-node cluster) and must be
// stopped during graceful shutdown.
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
		return mat, nil, nil
	}

	// Path 3: single-node cluster generates its own CA and node certificate.
	if opts.SingleNode {
		logger.Info("single-node mode, generating self-signed CA and node certificate")
		nodeURI := spiffe.NodeURI(opts.TrustDomain, opts.RaftAddress)
		mat, err := clustertls.GenerateCABundle(nodeURI)
		if err != nil {
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
		mat, err := Join(ctx, opts, logger)
		if err != nil {
			return nil, nil, err
		}
		return mat, nil, nil
	}

	// Path 5: first node of a multi-node cluster generates the CA, persists
	// it, and starts the Noise NK advertiser for peer enrollment.
	logger.Info("first node of multi-node cluster, generating CA and starting bootstrap servers")
	nodeURI := spiffe.NodeURI(opts.TrustDomain, opts.RaftAddress)
	mat, err = clustertls.GenerateCABundle(nodeURI)
	if err != nil {
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
// The returned Advertiser is non-nil only for the first-node path and must be
// stopped during graceful shutdown. Returns (nil, nil, nil, ExplicitFilesError)
// when any TLS*File option is set.
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
		return caCertPEM, caKeyPEM, nil, nil
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
		if err := clustertls.PersistCA(caCertPEM, caKeyPEM, opts.DataDir); err != nil {
			return nil, nil, nil, err
		}
		return caCertPEM, caKeyPEM, nil, nil
	}

	// Single node or first node: generate a new CA and persist it.
	if opts.SingleNode {
		logger.Info("single-node mode, generating self-signed CA")
	} else {
		logger.Info("first node of multi-node cluster, generating CA and starting bootstrap server")
	}
	caCertPEM, caKeyPEM, _, _, err = clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
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
		if err := os.WriteFile(keyPath, hexEncoded, 0o600); err != nil {
			noiseKey.Destroy()
			return nil, &ServerError{Protocol: "noise", Err: err}
		}
		logger.Info("generated and persisted Noise static key", "path", keyPath)
	}

	listenPort := opts.NoisePort
	if listenPort == 0 {
		listenPort = DefaultNoisePort
	}

	// The embedded DHKey aliases the protected private scalar; the Advertiser
	// owns the StaticKey from here and releases it in Stop.
	srv, err := noiseboot.NewServer(&noiseboot.ServerConfig{
		ListenAddr: "0.0.0.0:" + strconv.Itoa(listenPort),
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
// AdminPort). Returns an empty string if no address can be determined.
func ResolveServerURL(opts BootstrapOptions) string {
	// Explicit bootstrap address takes priority.
	if opts.ServerAddr != "" {
		return "https://" + opts.ServerAddr
	}

	// Extract host from the first static peer and combine with admin port.
	if opts.DiscoveryMethod == "static" && len(opts.Peers) > 0 {
		host := ExtractHostFromPeer(opts.Peers[0])
		if host != "" {
			port := opts.AdminPort
			if port == 0 {
				port = 8443
			}
			return "https://" + net.JoinHostPort(host, strconv.Itoa(port))
		}
	}

	// Fall back to DNS domain with admin port.
	if opts.DNSDomain != "" {
		port := opts.AdminPort
		if port == 0 {
			port = 8443
		}
		return "https://" + net.JoinHostPort(opts.DNSDomain, strconv.Itoa(port))
	}

	return ""
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
		serverURL := ResolveServerURL(opts.Bootstrap)
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
	adminPort := opts.AdminPort
	if adminPort == 0 {
		adminPort = 8443
	}
	slog.Info("bootstrap enrollment information for joining nodes",
		"noise_public_key", hex.EncodeToString(adv.noiseKey.Public),
		"noise_addr", adv.Addr(),
		"admin_port", adminPort,
	)
}

// fileExists returns true when path refers to an existing regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
