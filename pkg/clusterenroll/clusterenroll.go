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
//  4. Joining node: Bootstrap.HasFlags() true — fetches the anchor and
//     submits a CSR to an admitted member's join listener (JoinServer) when
//     BootstrapOptions.JoinConfigured(); otherwise refused with
//     *EnrollmentUnavailableError.
//  5. First node: multi-node cluster without bootstrap flags — generates CA and
//     starts the Noise NK advertiser for peer enrollment.
package clusterenroll

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"log/slog"
	"net"
	"net/url"
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
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	"github.com/jeremyhahn/go-qrdb/pkg/util/securefile"
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

	// DefaultJoinApprovalPollInterval is used when
	// BootstrapOptions.JoinApprovalPollInterval is zero: how often Join
	// re-presents a tokenless CSR while it sits in the signing member's
	// approval queue. Infrequent enough not to look like the brute-force
	// traffic AllowJoinAttempt rate-limits, frequent enough that an
	// operator's approval is picked up promptly.
	DefaultJoinApprovalPollInterval = 15 * time.Second
)

// readFileFn is os.ReadFile, indirected so a test can inject a read failure
// on a file that fileExists has already accepted. A mode-0000 file is not a
// substitute: CI job containers run as root, and root reads it successfully
// (conventions/testing-guidelines.md, Fault Injection).
var readFileFn = os.ReadFile

// newNoiseServer is noiseboot.NewServer, indirected so a test can observe the
// exact bundle provider this package hands the Noise server. What reaches the
// wire is also shaped by go-truststrap's handler, and this package's
// guarantee that no key material is served must hold on its own bytes.
var newNoiseServer = noiseboot.NewServer

// Options holds the parameters for cluster-TLS enrollment orchestration.
// Callers populate this from their own configuration (e.g., CLI flags or
// YAML config) before calling Resolve.
type Options struct {
	// RaftAddress is the Raft node address, used as the SPIFFE node identity.
	RaftAddress string

	// ReplicaID is this node's own Raft replica ID. Join sends it, with this
	// binary's claimed features (featuregate.SupportedNames), so the signing
	// member can publish a pre-membership feature advertisement for it --
	// see JoinRequest.ReplicaID. Zero means "unknown"; the signing member
	// then skips advertising rather than publish an unkeyed record.
	ReplicaID uint64

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

	// JoinAddr is the host:port of an admitted member's member-side
	// CSR-sign listener (JoinServer), for the Noise_NK transport. Required
	// for member-side join over Noise; see JoinConfigured.
	JoinAddr string

	// JoinKey is the hex Curve25519 public key of that listener
	// (JoinServer.PublicKey, hex-encoded). Required for member-side join
	// over Noise.
	JoinKey string

	// JoinHTTPURL is the base admin HTTPS URL of an admitted member (e.g.
	// "https://10.0.0.2:8443"), for the SPKI/Direct transport
	// (JoinHTTPPath). Required for member-side join over SPKI/Direct;
	// mutually exclusive with JoinAddr/JoinKey in practice, though both may
	// be configured -- Join prefers Noise when both are set.
	JoinHTTPURL string

	// JoinToken is the join-admission secret, shared by every join
	// transport. Empty means this node submits its CSR into the signing
	// member's replicated approval queue instead of presenting a token --
	// see JoinConfigured and JoinRequest's own doc comment -- a deliberate
	// configuration, not a missing one; there is no default secret.
	JoinToken string

	// JoinApprovalPollInterval bounds how often Join retries a tokenless
	// CSR submission while it sits in the approval queue (JoinToken == "").
	// Zero uses DefaultJoinApprovalPollInterval. Ignored when JoinToken is
	// set, since a token presentation is decided in one round trip.
	JoinApprovalPollInterval time.Duration
}

// JoinConfigured reports whether member-side CSR join is configured over
// EITHER transport: Noise (an admitted member's join listener address and
// public key) or SPKI/Direct (an admitted member's admin HTTPS URL, trusted
// the same way the matching anchor-fetch method already is). Unlike
// HasFlags (which selects path 4 at all), this is checked INSIDE Join to
// decide whether the join can proceed with a signed leaf or must refuse --
// a node joining via explicit TLS files, or one whose operator has not yet
// stood up a join route anywhere, takes the same path 4 selection without
// this being set.
//
// JoinToken is deliberately NOT required here: an empty token is a valid
// configuration, meaning "submit through the replicated approval queue"
// rather than "present an admission secret" -- see JoinRequest's own doc
// comment and docs/clustering/architecture.md's "Approval queue" section.
func (b BootstrapOptions) JoinConfigured() bool {
	return b.joinNoiseConfigured() || b.JoinHTTPURL != ""
}

// joinNoiseConfigured reports whether the Noise_NK join transport is
// configured, independent of the admission token.
func (b BootstrapOptions) joinNoiseConfigured() bool {
	return b.JoinAddr != "" && b.JoinKey != ""
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
	//
	// ZERO IS NOT ":0". It does not ask the kernel for an ephemeral port the
	// way a bare Go listener address would; it binds the fixed 63006. A
	// caller that wants a port nobody else holds -- a test, above all --
	// must reserve one and pass the number, because 63006 is the single
	// most contended port this package has: every concurrent copy of the
	// daemon or its tests on one host wants exactly it, and CI runs five
	// pipelines per host (go-qrdb#313, and go-qrdb#84 before it).
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

// Advertiser is a first node's enrollment surface. It is returned by every path
// that decides this node owes the cluster one, and must be stopped during
// graceful shutdown.
//
// It USUALLY holds a running Noise NK bootstrap server, which lets joining
// nodes fetch the cluster CA bundle using only the server's Curve25519 public
// key as the trust anchor. In FIPS 140-3 mode it holds no Noise server at all
// (see noiseAvailable), and a non-nil Advertiser whose NoiseEnabled reports
// false is the ordinary expected value there rather than a failure.
//
// A NON-NIL ADVERTISER MEANS "THIS NODE ADVERTISES ENROLLMENT", NOT "A NOISE
// LISTENER IS RUNNING". Callers gate on it accordingly: cmd/qrdbd uses it to
// decide whether to register the /v1/ca/bootstrap endpoint, which is the
// approved-method (SPKI-pinned TLS, DANE, direct HTTPS) half of the same
// surface. The two were the same pointer while Noise was the only advertise
// mechanism; collapsing them again would leave a FIPS first node unreachable by
// EVERY method rather than by one, silently, which is the go-qrdb#241 shape.
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

// NoiseEnabled reports whether this Advertiser is running a Noise NK listener.
// It is false on a nil Advertiser and on the Advertiser a first node gets in
// FIPS 140-3 mode, where the method is unavailable (see noiseAvailable) and
// joining nodes bootstrap over SPKI-pinned TLS, DANE or direct HTTPS instead.
//
// Callers that need to know whether Noise is being served ask this rather than
// testing the Advertiser pointer, which answers a different question -- see the
// Advertiser doc comment.
func (a *Advertiser) NoiseEnabled() bool {
	return a != nil && a.noiseSrv != nil
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

// EnrollmentUnavailableError is returned when this node is configured to
// join an existing cluster (bootstrap flags set). Joining is unavailable in
// this release, and the refusal is raised before any network dial and before
// anything touches the data directory.
//
// Earlier releases enrolled a joining node by handing it the cluster CA
// PRIVATE KEY -- over /v1/ca/bootstrap, a public route -- and letting it mint
// its own leaf. That made every node, and anyone who could reach a first
// node's admin port, a holder of the key that mints every cluster identity.
// The key is no longer distributed in any form, and member-side issuance --
// the joining node generates its key locally and submits a CSR that a running
// member signs -- has not shipped yet. Until it does, the only way to add a
// node is to issue its certificate on the node that holds the cluster CA and
// start it with the explicit TLS file flags.
type EnrollmentUnavailableError struct{}

// Error returns a human-readable description that names the remedy.
func (e *EnrollmentUnavailableError) Error() string {
	return "clusterenroll: joining an existing cluster is unavailable in this release: " +
		"the cluster CA private key is not distributed to joining nodes any more, and " +
		"member-side certificate issuance (the joining node submits a CSR that a running " +
		"member signs) has not shipped yet. To add a node now, issue its node certificate " +
		"(with its SPIFFE node URI SAN) on the node that holds the cluster CA and start " +
		"the new node with --tls-ca-file, --tls-cert-file and --tls-key-file instead of " +
		"bootstrap flags"
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

	// Path 4: joining an existing cluster. Refused in this release; see
	// EnrollmentUnavailableError.
	if opts.Bootstrap.HasFlags() {
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
//   - join:           refused with *EnrollmentUnavailableError. A joining
//     node never holds the cluster CA key, so it has none to return.
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

	// Join: a joining node never receives the cluster CA key, so there is no
	// CA signing key to resolve. Refused before any dial or write.
	if opts.Bootstrap.HasFlags() {
		return nil, nil, nil, &EnrollmentUnavailableError{}
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

// Join enrolls this node into an existing cluster (path 4): it fetches the
// trust anchor (the CA certificate only), generates its own key pair, and
// submits a CSR to an admitted member's join listener for signing. The
// cluster CA private key never reaches this node -- it is never read, only
// used to sign, on whichever member answers the join request.
//
// Refused with *EnrollmentUnavailableError before any network dial and
// before anything is written to opts.DataDir when member-side join is not
// configured (BootstrapOptions.JoinConfigured reports false) -- e.g. a node
// started with only an anchor-fetch method and no join listener address, or
// one meant to be enrolled via explicit TLS files instead. A refused join
// leaves no role file and no local key material.
func Join(ctx context.Context, opts *Options, logger *slog.Logger) (*clustertls.Material, error) {
	if !opts.Bootstrap.JoinConfigured() {
		return nil, &EnrollmentUnavailableError{}
	}

	anchorPEM, err := FetchAnchor(ctx, opts, logger)
	if err != nil {
		return nil, err
	}

	nodeKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, &clustertls.GenerateError{Err: err}
	}
	nodeURI := spiffe.NodeURI(opts.TrustDomain, opts.RaftAddress)
	csrDER, err := buildJoinCSR(nodeKey, nodeURI)
	if err != nil {
		return nil, err
	}

	certPEM, err := joinCSRWithApprovalRetry(ctx, opts, csrDER, nodeURI, logger)
	if err != nil {
		return nil, err
	}

	if err := verifyJoinedLeaf(anchorPEM, certPEM, &nodeKey.PublicKey); err != nil {
		return nil, err
	}

	keyDER, err := x509.MarshalECPrivateKey(nodeKey)
	if err != nil {
		return nil, &clustertls.GenerateError{Err: err}
	}
	mat := &clustertls.Material{
		CACertPEM: anchorPEM,
		CertPEM:   certPEM,
		KeyPEM:    pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}

	// Recorded before the material, same ordering invariant persistRole
	// documents: a crash between the two leaves a role file this boot will
	// not reach (RoleJoined serves no enrollment listener, so there is
	// nothing go-qrdb#241 would miss), never material with no role beside it.
	if err := persistRole(opts.DataDir, RoleJoined); err != nil {
		return nil, err
	}
	if err := clustertls.PersistMaterial(mat, opts.DataDir); err != nil {
		return nil, err
	}
	logger.Info("joined existing cluster", "dir", opts.DataDir)
	return mat, nil
}

// joinApprovalPollInterval returns the configured poll interval or
// DefaultJoinApprovalPollInterval.
func (b BootstrapOptions) joinApprovalPollInterval() time.Duration {
	if b.JoinApprovalPollInterval <= 0 {
		return DefaultJoinApprovalPollInterval
	}
	return b.JoinApprovalPollInterval
}

// joinCSRWithApprovalRetry submits csrDER to the configured join transport
// and returns the signed leaf. A token presentation (opts.Bootstrap.
// JoinToken != "") is decided in one round trip, exactly as before. A
// tokenless submission (the approval-queue path) may come back as
// *JoinPendingError instead -- not a failure, the signing member's report
// that the CSR is queued -- in which case this re-presents the IDENTICAL
// CSR after joinApprovalPollInterval, indefinitely, until the operator
// approves it (a signed leaf), rejects it (a terminal *JoinRefusedError),
// or ctx is done. A node blocked here is doing exactly what an operator
// configuring a tokenless join chose: wait for a human decision rather than
// crash-loop the whole daemon start every few seconds.
func joinCSRWithApprovalRetry(ctx context.Context, opts *Options, csrDER []byte, nodeURI string, logger *slog.Logger) ([]byte, error) {
	for {
		var certPEM []byte
		var err error
		if opts.Bootstrap.joinNoiseConfigured() {
			joinKey, kerr := hex.DecodeString(opts.Bootstrap.JoinKey)
			if kerr != nil {
				return nil, &JoinKeyDecodeError{Err: kerr}
			}
			logger.Info("joining existing cluster via member-side CSR (Noise_NK)",
				"join_addr", opts.Bootstrap.JoinAddr)
			certPEM, err = JoinCSR(ctx, &JoinClientOptions{
				ServerAddr: opts.Bootstrap.JoinAddr,
				ServerKey:  joinKey,
				Token:      opts.Bootstrap.JoinToken,
				ReplicaID:  opts.ReplicaID,
				Features:   featuregate.SupportedNames(),
			}, csrDER, nodeURI)
		} else {
			logger.Info("joining existing cluster via member-side CSR (SPKI/Direct)",
				"join_url", opts.Bootstrap.JoinHTTPURL)
			certPEM, err = JoinCSRHTTP(ctx, &JoinHTTPClientOptions{
				ServerURL: opts.Bootstrap.JoinHTTPURL,
				SPKIPin:   opts.Bootstrap.SPKIPin,
				Token:     opts.Bootstrap.JoinToken,
				ReplicaID: opts.ReplicaID,
				Features:  featuregate.SupportedNames(),
			}, csrDER, nodeURI)
		}
		if err == nil {
			return certPEM, nil
		}

		var pending *JoinPendingError
		if !errors.As(err, &pending) {
			return nil, err
		}
		logger.Info("member-side CSR join: awaiting operator approval",
			"request_id", pending.RequestID, "retry_in", opts.Bootstrap.joinApprovalPollInterval())
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(opts.Bootstrap.joinApprovalPollInterval()):
		}
	}
}

// verifyJoinedLeaf proves the leaf a signing member returned is the one
// this joiner actually asked for: it chains to the SAME anchor this joiner
// fetched (anchorPEM, step 1 of the join), and carries the public half of
// the private key generated for it (pub) -- never a leaf for a different
// key the signing member (or a man in the middle that reached the join
// transport but not the anchor fetch) substituted. Member-side CSR signing
// takes nothing but the public key from the CSR (buildJoinCSR's own doc
// comment), so this is the joiner's only check that what came back actually
// answers what it asked.
func verifyJoinedLeaf(anchorPEM, leafPEM []byte, pub *ecdsa.PublicKey) error {
	anchorBlock, _ := pem.Decode(anchorPEM)
	if anchorBlock == nil {
		return &JoinedLeafVerifyError{Reason: "anchor certificate is not valid PEM"}
	}
	anchorCert, err := x509.ParseCertificate(anchorBlock.Bytes)
	if err != nil {
		return &JoinedLeafVerifyError{Reason: "anchor certificate: " + err.Error()}
	}

	leafBlock, _ := pem.Decode(leafPEM)
	if leafBlock == nil {
		return &JoinedLeafVerifyError{Reason: "issued leaf is not valid PEM"}
	}
	leafCert, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		return &JoinedLeafVerifyError{Reason: "issued leaf: " + err.Error()}
	}

	if err := leafCert.CheckSignatureFrom(anchorCert); err != nil {
		return &JoinedLeafVerifyError{Reason: "issued leaf does not chain to the fetched anchor: " + err.Error()}
	}
	leafPub, ok := leafCert.PublicKey.(*ecdsa.PublicKey)
	if !ok || !leafPub.Equal(pub) {
		return &JoinedLeafVerifyError{Reason: "issued leaf does not carry this joiner's own public key"}
	}
	return nil
}

// JoinedLeafVerifyError is returned when the leaf a signing member returns
// does not chain to the anchor this joiner fetched, or does not carry the
// public key generated for the join -- a signing-member or transport
// defect, never persisted to disk (clustertls.PersistMaterial is not
// reached).
type JoinedLeafVerifyError struct {
	Reason string
}

// Error returns a human-readable description.
func (e *JoinedLeafVerifyError) Error() string {
	return "clusterenroll: issued leaf verification failed: " + e.Reason
}

// buildJoinCSR creates a PKCS#10 CertificateRequest for key, self-signed to
// prove possession. Its Subject and SANs are informational only -- the
// signing member's IssueLeafForKey builds the issued leaf's actual template
// (clustertls.NodeLeafTemplate) from scratch and takes nothing from the CSR
// but its public key, exactly as every CeremonyCA in this codebase does.
func buildJoinCSR(key *ecdsa.PrivateKey, nodeURI string) ([]byte, error) {
	template := &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "qrdb-node"},
	}
	if nodeURI != "" {
		u, err := url.Parse(nodeURI)
		if err != nil {
			return nil, &clustertls.GenerateError{Err: err}
		}
		template.URIs = []*url.URL{u}
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, template, key)
	if err != nil {
		return nil, &clustertls.GenerateError{Err: err}
	}
	return der, nil
}

// JoinKeyDecodeError is returned when BootstrapOptions.JoinKey is not valid
// hex.
type JoinKeyDecodeError struct {
	Err error
}

// Error returns a human-readable description.
func (e *JoinKeyDecodeError) Error() string {
	return "clusterenroll: join key is not valid hex: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *JoinKeyDecodeError) Unwrap() error { return e.Err }

// StartAdvertiser starts the Noise NK bootstrap server so that joining nodes
// can securely fetch the cluster CA CERTIFICATE. It serves mat.CACertPEM and
// nothing else: mat.CAKeyPEM is never read here, and material whose CA
// certificate field holds no certificate (or holds key material) is refused
// with *AnchorError before any listener starts. The server's static key is
// persisted to {dataDir}/noise-key.hex for reuse across restarts; if the key
// file already exists it is loaded instead of generating a new one.
//
// This is the path 5 server-start implementation, reached in production through
// Resolve. It is exported for callers that already hold CA material and need
// only the advertiser.
//
// It used to also back cmd/qrdbd's startBootstrapServers, a thin wrapper that
// no production path called and that was deleted as dead code (go-qrdb#509).
// That wrapper's tests had been the only coverage of two failure branches here
// -- an unusable data directory and an occupied port -- without ever counting
// toward this package's coverage, since a test binary is credited only with
// its own package. They now live in this package's tests.
func StartAdvertiser(mat *clustertls.Material, opts AdvertiseOptions, dataDir string, logger *slog.Logger) (*Advertiser, error) {
	if err := ValidateAnchorBundle(mat.CACertPEM); err != nil {
		return nil, err
	}
	return startNoiseAdvertiser(&anchorBundleProvider{anchorPEM: mat.CACertPEM}, opts, dataDir, logger)
}

// startNoiseAdvertiser starts the Noise NK bootstrap server with the given
// bundle provider, persisting (or reusing) the static Noise key at
// {dataDir}/noise-key.hex. It is the shared implementation behind
// StartAdvertiser (the cluster CA certificate) and StartAnchorAdvertiser
// (an external trust-anchor chain); both serve certificates only.
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

	// FIPS 140-3 mode: this node still advertises enrollment, over the approved
	// methods only. It is NOT a refusal, because the operator never chose Noise
	// here -- there is no advertise-method option to choose; NoisePort and
	// NoiseHost say where a listener binds, not whether enrollment happens.
	// What was asked for is "be the first node of a multi-node cluster", and
	// that is still satisfiable: SPKI-pinned TLS, DANE and direct HTTPS all
	// fetch /v1/ca/bootstrap from this node's admin server, which cmd/qrdbd
	// registers for exactly the nodes that get an Advertiser back.
	//
	// Refusing to boot instead would mean go-qrdb cannot form a multi-node
	// cluster under FIPS at all, for a method that is not the one a FIPS
	// deployment can use anyway. Returning nil instead -- "no advertiser" --
	// would be worse than either: nil is how this package says "this node owes
	// the cluster nothing", and a first node saying that is the silently
	// unjoinable node of go-qrdb#241.
	//
	// It is not a silent downgrade. The advisory is logged at WARN here, the
	// caller logs it again through logAdvertiserInfo with the admin port the
	// joining nodes need, and the state is readable afterwards through
	// Advertiser.NoiseEnabled.
	if !noiseAvailable() {
		logger.Warn(fipsNoiseAdvisory+"; this node advertises enrollment over the approved "+
			"methods only, so joining nodes must use SPKI-pinned TLS, DANE or direct HTTPS "+
			"against its admin endpoint rather than --bootstrap-noise-key",
			"admin_port", opts.AdminPort,
		)
		return &Advertiser{}, nil
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
		if err := securefile.WriteFileAtomic(keyPath, hexEncoded, 0o600); err != nil {
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
	srv, err := newNoiseServer(&noiseboot.ServerConfig{
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
// FetchAnchor is its caller, and validates that what arrived is
// certificates only.
func autoFetchBundle(ctx context.Context, opts *Options, logger *slog.Logger) ([]byte, error) {
	cfg, err := buildAutoConfig(opts, logger)
	if err != nil {
		return nil, err
	}

	fetchCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	resp, err := truststrap.AutoFetch(fetchCtx, cfg)
	if err != nil {
		return nil, &FetchError{Method: "auto", Err: err}
	}

	return resp.BundlePEM, nil
}

// buildAutoConfig turns BootstrapOptions into a truststrap AutoConfig and the
// ordered candidate method list, applying the FIPS 140-3 ruling on the way.
//
// It is separated from autoFetchBundle so the selection can be asserted without
// a network: the ruling it implements is the whole of this change, and a test
// that had to stand up a bootstrap server to read the method list would be
// measuring the server rather than the selector.
//
// The FIPS ruling has two halves and the difference between them is the
// operator's stated intent:
//
//   - Noise configured ALONGSIDE an approved method. The operator supplied a
//     preference list and truststrap.AutoFetch is a fallback chain by
//     construction, so "any of these" is what was asked for. Noise is dropped
//     from the list, loudly, and the approved methods carry the join.
//   - Noise configured as the ONLY method. There is nothing to select among:
//     the operator named Noise, and this runtime cannot provide it. That is
//     refused here -- before the first network dial and before anything touches
//     the data directory -- rather than after a 15-second per-method timeout
//     ending in a message about a key size.
//
// Neither half is a silent fallback, which is the one shape this must not take.
func buildAutoConfig(opts *Options, logger *slog.Logger) (*truststrap.AutoConfig, error) {
	cfg := &truststrap.AutoConfig{
		PerMethodTimeout: DefaultTimeout,
		Logger:           logger,
	}

	var methods []truststrap.Method

	// Configure Noise NK if both key and address are provided.
	noiseConfigured := opts.Bootstrap.NoiseKey != "" && opts.Bootstrap.NoiseAddr != ""
	noiseDroppedForFIPS := noiseConfigured && !noiseAvailable()
	if noiseDroppedForFIPS {
		logger.Warn(fipsNoiseAdvisory+"; the configured Noise bootstrap server is excluded "+
			"from this node's join attempt",
			"noise_addr", opts.Bootstrap.NoiseAddr,
		)
	}
	if noiseConfigured && noiseAvailable() {
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

	// Refused only when FIPS is what emptied the list. An empty list arrived at
	// any other way -- a SPKIPin with no ServerAddr, say -- keeps the behaviour
	// it has always had, because this ruling has nothing to say about it and a
	// FIPS error on a configuration FIPS did not reject would be a wrong answer
	// that happens to fail.
	if len(methods) == 0 && noiseDroppedForFIPS {
		return nil, &NoiseFIPSUnavailableError{Operation: "this node's cluster enrollment join"}
	}

	if len(methods) > 0 {
		cfg.MethodOrder = methods
	}

	return cfg, nil
}

// logAdvertiserInfo logs the information operators need to configure joining
// nodes: the Noise public key (hex-encoded), the bootstrap server address, and
// the admin server port.
//
// In FIPS 140-3 mode there is no Noise key to report, and this says so rather
// than returning quietly. A first node that advertises fewer methods than its
// operator expects has to announce the fact at the moment it decides: the only
// other thing that ever reports it is a peer's join failing on another machine,
// which is the failure mode go-qrdb#241 exists to keep out of this package.
func logAdvertiserInfo(adv *Advertiser, opts AdvertiseOptions) {
	if adv == nil {
		return
	}
	// No Noise identity is the FIPS 140-3 case, and it is the ONLY case: the
	// advertiser is constructed with a key whenever it is constructed with a
	// server, so "no key" and "no listener" are one condition and are tested as
	// one. A second, separately-reachable-looking guard here would be code that
	// cannot run.
	if adv.noiseKey == nil {
		slog.Warn(fipsNoiseAdvisory+"; no Noise public key is advertised. Joining nodes must "+
			"bootstrap over an approved method -- SPKI-pinned TLS, DANE or direct HTTPS -- "+
			"against this node's /v1/ca/bootstrap endpoint",
			"admin_port", opts.AdminPort,
		)
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
