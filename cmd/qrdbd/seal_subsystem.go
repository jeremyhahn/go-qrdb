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
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// sealSubsystem aggregates the storage barrier, credential service, and
// barrier registry that must be constructed BEFORE the data-plane host
// when seal is enabled. Pebble.Open on the data plane wraps its vfs.FS
// with seal/encfs and would fail with ErrSealed if attempted while the
// barrier is sealed; the orchestrator therefore builds these objects
// first, brings up the sealed-window listener so operators can drive
// the unseal API, and only then constructs the host.
type sealSubsystem struct {
	// SealStore is the dedicated PebbleDB instance that backs the seal
	// sub-store (wrapped barrier root key, sealing strategy blobs, the
	// format-version sentinel). It is plaintext by design because it
	// must be readable BEFORE the barrier is unsealed.
	SealStore *pebblestore.PebbleStorage

	// SealBarrier is the unsealable storage barrier. Callers MUST check
	// SealBarrier.IsSealed() before any operation that requires the
	// data plane.
	SealBarrier *seal.StorageBarrier

	// CredCoreSvc is the core credential service used by ceremony
	// flows and the credential REST/gRPC handlers.
	CredCoreSvc *credentials.CredentialService

	// BarrierRegistry manages per-tenant barriers that are layered on
	// top of the system barrier.
	BarrierRegistry *seal.BarrierRegistry

	// PINBackend holds the node's SO and User PINs, persisted in the seal
	// sub-store so they are readable while the barrier is sealed.
	PINBackend pin.PINBackend

	// SealDataDir is the directory containing the seal sub-store. It
	// is exposed so callers (e.g. ceremony service construction) can
	// reuse the same path.
	SealDataDir string
}

// Close releases the resources held by the seal subsystem in reverse
// order of acquisition. It is safe to call multiple times — the
// underlying Pebble store guards against double-close.
func (s *sealSubsystem) Close() error {
	if s == nil || s.SealStore == nil {
		return nil
	}
	return s.SealStore.Close()
}

// initSealSubsystem builds the seal storage barrier, sealing strategy,
// credential service, and barrier registry from the supplied flags.
// It is called BEFORE host construction so that the data-plane Pebble
// instance can later be opened with store.WithEncryptor pointing at
// the returned barrier.
//
// complianceAudit is the node's compliance trail, opened before this
// subsystem: the system barrier records its events on it (keyed by the
// system tenant), and so does every tenant barrier the registry builds
// (keyed by its tenant). Nil records nothing.
//
// The caller is responsible for invoking Close on the returned
// subsystem when the daemon is shutting down. On error the function
// closes any partially-allocated resources before returning.
func initSealSubsystem(f *startFlags, sigCtx context.Context, complianceAudit audit.AuditLogger) (*sealSubsystem, error) {
	sealDataDir := filepath.Join(f.dataDir, "seal")
	if err := os.MkdirAll(sealDataDir, 0o700); err != nil {
		return nil, &SealStorageError{Err: err}
	}

	// SyncWrites is forced on: this store is the sole persisted copy of the
	// root-key blob and epoch/rotation state, not a Raft-replicated store,
	// so an unsynced write can be lost on crash even though it appeared to
	// succeed — orphaning every file the lost root key had encrypted. See
	// the equivalent comment in sdk/go/node's buildNodeSealSubsystem.
	sealStore, err := pebblestore.New(&pebblestore.Config{DataDir: sealDataDir, SyncWrites: true})
	if err != nil {
		return nil, &SealStorageError{Err: err}
	}

	sealBackend := seal.NewStorageAdapter(sealStore)
	barrierCfg := seal.BarrierConfig{
		RootKeyPath:  f.sealConfig.RootKeyPath,
		DeploymentID: f.deploymentID,
		AuditLogger:  seal.NewComplianceAuditLogger(complianceAudit, tenantid.System, seal.SystemBarrierAuditName),
	}

	// Reconcile a system-scope enable-encryption interrupted by a crash, BEFORE
	// the barrier is built or any data store is opened: roll a committed migration
	// forward (keyed data + keyed root key both in place) or a not-yet-committed
	// one back. Idempotent + credential-free, so safe on every boot; a no-op when
	// no migration journal is present. Covers the dirs recodeSystemBarrier swaps —
	// the host state-machine Pebble, the vector store, and the Raft WAL — so a
	// crash in the swap→adopt window can no longer wedge boot with a keyed-data /
	// none-root-key mismatch. Running it before PersistedStrategyFrom (below) means
	// a rolled-forward migration is then seen as keyed and unsealed correctly.
	sysStoreDirs := []string{
		filepath.Join(f.dataDir, "nodehost", "snapshots", fmt.Sprintf("shard-%d", shard.ShardKV)),
		filepath.Join(f.dataDir, "vector"),
		filepath.Join(f.dataDir, "wal"),
	}
	if rerr := database.RecoverInterruptedMigration(sigCtx, sealBackend, barrierCfg.RootKeyPath, sysStoreDirs); rerr != nil {
		slog.Warn("system encryption-migration crash recovery failed; manual reconcile may be required",
			slog.Any("error", rerr))
	}

	var strategies []seal.SealingStrategy
	switch f.sealConfig.Strategy {
	case "shamir":
		shamirStrat, shamirErr := seal.NewShamirStrategy(
			f.sealConfig.ShamirThreshold, f.sealConfig.ShamirTotal)
		if shamirErr != nil {
			_ = sealStore.Close()
			return nil, &SealStrategyError{Strategy: "shamir", Err: shamirErr}
		}
		strategies = append(strategies, shamirStrat)
		shareTTL, ttlErr := f.sealConfig.ShareTTLDuration()
		if ttlErr != nil {
			_ = sealStore.Close()
			return nil, &SealStrategyError{Strategy: "shamir", Err: ttlErr}
		}
		barrierCfg.Shamir = &seal.ShamirConfig{
			Threshold:   f.sealConfig.ShamirThreshold,
			TotalShares: f.sealConfig.ShamirTotal,
			QuorumTTL:   shareTTL,
		}
	case "none":
		// Barrier-present-but-unencrypted: the at-rest store is wrapped in
		// zero-epoch (plaintext) format. Initialized with empty credentials, it
		// boots serving and can later be migrated to a keyed strategy in place
		// via `qrdb barrier enable-encryption` (handleSystemMigration). This is
		// the canonical source state for enabling encryption after the fact.
		strategies = append(strategies, seal.NewNoneStrategy())
	default: // "passphrase"
		// Deferred software (passphrase) strategy: the admin supplies the
		// passphrase at BarrierInitialize / Unseal time and it is run through the
		// KDF. There is no construction-time placeholder, so the barrier is never
		// sealed with a deployment-derivable key. (Matches the Software factory in
		// pkg/seal/strategy_registry.go.)
		strategies = append(strategies, seal.NewDeferredSoftwareStrategy())
	}

	// Restart-survival for enable-encryption: if the persisted root-key blob was
	// migrated to a keyed strategy after the fact (the daemon was started with
	// strategy "none" but `qrdb barrier enable-encryption` since rewrote the
	// canonical root key to passphrase/shamir), ALSO register the migrated
	// strategy so the in-process re-init — or a later process restart — can unseal
	// it. The static config still names the pre-migration strategy; the barrier is
	// self-describing from its persisted state. Mirrors the SDK node's
	// buildNodeSealSubsystem restart-survival path.
	if persisted := seal.PersistedStrategyFrom(sigCtx, sealBackend, barrierCfg.RootKeyPath); persisted != "" && !sealStrategyRegistered(strategies, persisted) {
		switch persisted {
		case seal.StrategyPassphrase:
			strategies = append(strategies, seal.NewDeferredSoftwareStrategy())
		case seal.StrategyShamir:
			// none->shamir requires the M-of-N params, which are not in a "none"
			// config; register only when they are available (a real shamir config
			// or a daemon whose config carried them). Best-effort.
			if f.sealConfig.ShamirThreshold >= 2 && f.sealConfig.ShamirTotal >= f.sealConfig.ShamirThreshold {
				if shamirStrat, e := seal.NewShamirStrategy(f.sealConfig.ShamirThreshold, f.sealConfig.ShamirTotal); e == nil {
					strategies = append(strategies, shamirStrat)
				}
			}
		}
	}

	// Explicit strategy selection (no implicit preference-order default):
	// the preference order is exactly the strategies registered above.
	for _, s := range strategies {
		barrierCfg.PreferenceOrder = append(barrierCfg.PreferenceOrder, s.ID())
	}

	sealBarrier, barrierErr := seal.NewStorageBarrier(
		slog.Default(), sealBackend, barrierCfg, strategies...)
	if barrierErr != nil {
		_ = sealStore.Close()
		return nil, &SealBarrierError{Err: barrierErr}
	}

	// Format-version sentinel guards against running this build
	// against a data dir produced by a pre-encfs version that wrote
	// plaintext sstables. EnsureFormatSentinel writes the current
	// version on a genuinely empty seal sub-store, reads and validates
	// it on subsequent boots, and returns seal.ErrLegacyDataDir when
	// the sentinel is absent on a non-empty store. The seal sub-store
	// is empty IFF it currently has no keys at all (the sentinel has
	// not yet been written and no strategy has persisted blobs).
	sentinelKeys, listErr := sealBackend.List(sigCtx, "")
	if listErr != nil {
		_ = sealStore.Close()
		return nil, &SealBarrierError{Err: listErr}
	}
	if sentinelErr := seal.EnsureFormatSentinel(sigCtx, sealBackend, len(sentinelKeys) == 0); sentinelErr != nil {
		_ = sealStore.Close()
		// A legacy (pre-encfs) data dir is an operator-actionable refusal, not
		// a generic barrier failure. There is no in-place upgrade path, so the
		// message has to name the directory the operator must act on and say
		// what was detected; SealBarrierError replaced all of that with
		// "seal barrier initialization failed", which tells an operator at 3am
		// nothing they can use. LegacyDataDirError was declared for exactly
		// this and nothing returned it (go-qrdb#243). It unwraps to the
		// sentinel, so errors.Is(err, seal.ErrLegacyDataDir) still matches.
		if errors.Is(sentinelErr, seal.ErrLegacyDataDir) {
			return nil, &LegacyDataDirError{DataDir: f.dataDir, Err: sentinelErr}
		}
		return nil, &SealBarrierError{Err: sentinelErr}
	}

	// Build credential service for ceremony support.
	credStrategy := f.sealConfig.CredentialStrategy
	if credStrategy == "" {
		credStrategy = "barrier"
	}
	credCoreSvc, credErr := credentials.NewCredentialService(
		&credentials.Config{Strategy: credStrategy}, sealBarrier, slog.Default())
	if credErr != nil {
		_ = sealStore.Close()
		return nil, &CredentialServiceError{Err: credErr}
	}

	// Initialize barrier registry for multi-tenant encryption isolation.
	// The system barrier is the root; tenant barriers are created on
	// demand when tenants are provisioned via the TenantService API.
	barrierRegistry, regErr := seal.NewBarrierRegistry(slog.Default(), sealBarrier, seal.WithAuditTrail(complianceAudit))
	if regErr != nil {
		_ = sealStore.Close()
		return nil, &BarrierRegistryError{Err: regErr}
	}

	pinBackend, pinErr := lifecycle.NewSealStorePINBackend(sealBackend)
	if pinErr != nil {
		_ = sealStore.Close()
		return nil, &SealStorageError{Err: pinErr}
	}

	slog.Info("seal subsystem initialized",
		slog.String("strategy", f.sealConfig.Strategy),
		slog.String("data_dir", sealDataDir))

	return &sealSubsystem{
		SealStore:       sealStore,
		SealBarrier:     sealBarrier,
		CredCoreSvc:     credCoreSvc,
		BarrierRegistry: barrierRegistry,
		PINBackend:      pinBackend,
		SealDataDir:     sealDataDir,
	}, nil
}

// ceremonyCAFromBundle builds the CeremonyCA used to sign Security
// Officer CSRs from the node's TLS bundle, for the pre-store bootstrap
// window only: BuildSealServicesOnly runs before the KV service exists, so
// there is no cakey.SoftwareProvider (or injected HSM/TPM/KMS provider) to
// resolve Key A through yet, and this bundle's in-memory key is the only
// material available. It is wrapped in a cakey.StaticProvider -- itself a
// cakey.Provider -- and signed through via ceremony.NewProviderCA, the
// SAME CeremonyCA type every other Key A consumer uses once the provider
// seam is reachable, rather than ceremony.NewLocalCA's own independent
// parse-and-hold-raw-PEM path. lifecycle.BuildServices overrides this with
// a FRESH ceremony.NewProviderCA(n.ClusterCAProv) once the replicated
// store -- and so the real, cluster-wide Key A provider -- is live; see its
// own doc comment.
//
// Returns (nil, nil) when the bundle carries no CA private key: with
// BYO TLS certificates the daemon cannot sign on the operator's CA's
// behalf, and officer registration correctly fails with
// ErrCANotInitialized until a CA is established via the full
// initialization ceremony.
func ceremonyCAFromBundle(bundle *TLSBundle) (ceremony.CeremonyCA, error) {
	if bundle == nil || len(bundle.CAKeyPEM) == 0 {
		return nil, nil
	}

	certBlock, _ := pem.Decode(bundle.CACertPEM)
	if certBlock == nil {
		return nil, &CeremonyCAError{Err: &ceremony.CACertParseError{Err: errNoPEMBlockInCACert}}
	}
	caCert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, &CeremonyCAError{Err: &ceremony.CACertParseError{Err: err}}
	}

	keyBlock, _ := pem.Decode(bundle.CAKeyPEM)
	if keyBlock == nil {
		return nil, &CeremonyCAError{Err: &ceremony.CAKeyParseError{Err: errNoPEMBlockInCAKey}}
	}
	caKey, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, &CeremonyCAError{Err: &ceremony.CAKeyParseError{Err: err}}
	}

	provider, err := cakey.NewStaticProvider(caKey, caCert)
	if err != nil {
		return nil, &CeremonyCAError{Err: err}
	}
	ca, err := ceremony.NewProviderCA(provider)
	if err != nil {
		return nil, &CeremonyCAError{Err: err}
	}
	return ca, nil
}

// errNoPEMBlockInCACert and errNoPEMBlockInCAKey name a bundle's bootstrap
// CA material that does not decode as PEM, matching the messages
// ceremony.NewLocalCA's own parsing used -- wrapped here by the same typed
// *ceremony.CACertParseError / *ceremony.CAKeyParseError callers already
// match on, even though construction now goes through
// cakey.StaticProvider + ceremony.NewProviderCA rather than NewLocalCA.
var (
	errNoPEMBlockInCACert = errors.New("no PEM block in CA certificate")
	errNoPEMBlockInCAKey  = errors.New("no PEM block in CA key")
)

// awaitUnseal brings up the pre-host sealed-window listener: an
// API-only multi-protocol server that exposes ONLY the seal-related
// endpoints (barrier status / initialize / unseal, ceremony, etc.)
// gated by the sealed-window middleware. The function blocks until the
// barrier is unsealed (operator-driven) or sigCtx is canceled
// (SIGINT/SIGTERM), then stops the listener cleanly and returns the
// seal-service Node so the post-unseal boot can reuse its instances.
//
// Why this exists: with seal/encfs enabled, the data-plane Pebble
// instance cannot be opened while the barrier is sealed because every
// file open returns seal.ErrSealed. The host therefore cannot start
// until unseal completes — but operators need the unseal API itself to
// be reachable BEFORE the host comes up. This listener fills that
// boot-time gap.
//
// The returned Node MUST be threaded into the full-boot BuildServices
// call (via Infra.CeremonyCoreSvc): officer registrations performed
// during the sealed window are cache-only (the persistent registry is
// barrier-encrypted) and distributed shares live in the ceremony
// service's in-memory enclaves — discarding the instance would orphan
// both. For the same reason carried, the ceremony service an in-process
// migration carried out of the previous boot iteration (nil on a cold
// boot), is reused here rather than replaced.
//
// complianceAudit is the node's compliance audit trail, already open: the
// holder events of the window -- envelope claims, quorum submissions -- are
// recorded on it.
func awaitUnseal(sigCtx context.Context, f *startFlags, bundle *TLSBundle, sub *sealSubsystem, ceremonyCA ceremony.CeremonyCA, carried *ceremony.CeremonyService, complianceAudit audit.AuditLogger) (*lifecycle.Node, error) {
	auditPolicy, err := audit.ParseFailurePolicy(f.auditFailurePolicy)
	if err != nil {
		return nil, err
	}
	sealNode, err := lifecycle.BuildSealServicesOnly(sigCtx, lifecycle.Infra{
		CeremonyCoreSvc:           carried,
		AuditLogger:               complianceAudit,
		AuditFailurePolicy:        auditPolicy,
		DataDir:                   f.dataDir,
		SealBarrier:               sub.SealBarrier,
		BarrierRegistry:           sub.BarrierRegistry,
		CredentialCoreSvc:         sub.CredCoreSvc,
		CeremonyDataDir:           sub.SealDataDir,
		CeremonyCA:                ceremonyCA,
		PINBackend:                sub.PINBackend,
		Logger:                    slog.Default(),
		DefaultTenantQuotas:       f.tenantConfig.DefaultQuotas.ToQuotas(),
		DefaultTenantEntitlements: f.tenantConfig.DefaultEntitlements.ToEntitlements(),
	})
	if err != nil {
		return nil, &SealedListenerStartupError{Err: err}
	}

	serverCfg, err := buildServerConfig(f)
	if err != nil {
		return nil, err
	}
	serverOpts := []server.ServerOption{
		server.WithTLS(bundle.TLSConfig),
		server.WithCACertPool(bundle.CACertPool),
	}
	if ceremonyCA != nil {
		serverOpts = append(serverOpts, server.WithCeremonyCA(ceremonyCA))
	}
	if sealNode.Barrier != nil {
		serverOpts = append(serverOpts, server.WithBarrierService(sealNode.Barrier))
	}
	if sub.PINBackend != nil {
		serverOpts = append(serverOpts, server.WithPINBackend(sub.PINBackend))
	}
	if sealNode.Ceremony != nil {
		serverOpts = append(serverOpts, server.WithCeremonyService(sealNode.Ceremony))
	}
	if sealNode.Credential != nil {
		serverOpts = append(serverOpts, server.WithCredentialService(sealNode.Credential))
	}
	if sub.BarrierRegistry != nil {
		serverOpts = append(serverOpts, server.WithBarrierRegistry(sub.BarrierRegistry))
	}

	slog.Info("starting sealed-window listener",
		slog.String("admin_port", strconvI(f.adminPort)),
		slog.String("grpc_port", strconvI(f.grpcPort)))

	// server.ServeSealedWindow blocks until the operator drives the unseal
	// API (returns nil) or sigCtx is canceled by SIGINT/SIGTERM (returns
	// *server.SealedWindowCanceledError). The seal-service instances in
	// serverOpts survive the listener teardown; the returned sealNode is
	// threaded into the full-boot BuildServices so ceremony enclave state
	// is preserved.
	if err := server.ServeSealedWindow(sigCtx, serverCfg, sub.SealBarrier, serverOpts...); err != nil {
		var canceled *server.SealedWindowCanceledError
		if errors.As(err, &canceled) {
			slog.Info("shutdown signal received during unseal wait")
			// Returning errAwaitUnsealCanceled lets runStart treat
			// shutdown-during-unseal as a clean exit (return nil to
			// the cobra runner) instead of surfacing context.Canceled
			// as an error code. This matches the existing handling
			// for other "canceled by shutdown signal" sites in
			// runStart.
			return nil, errAwaitUnsealCanceled
		}
		return nil, &SealedListenerStartupError{Err: err}
	}

	slog.Info("barrier unsealed; continuing boot")
	return sealNode, nil
}

// errAwaitUnsealCanceled is the sentinel returned by awaitUnseal when
// SIGINT/SIGTERM is received during the unseal wait. runStart treats
// it as a clean shutdown rather than an abnormal failure.
var errAwaitUnsealCanceled = &awaitUnsealCanceledError{}

// awaitUnsealCanceledError is the typed sentinel error indicating that
// the daemon's pre-host unseal wait was terminated by a shutdown
// signal rather than by the operator driving the unseal API.
type awaitUnsealCanceledError struct{}

// Error implements the error interface.
func (e *awaitUnsealCanceledError) Error() string {
	return "qrdbd: unseal wait canceled by shutdown signal"
}

// strconvI returns the decimal representation of an int. Kept local to
// avoid pulling strconv into seal_subsystem.go solely for log lines.
func strconvI(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// sealStrategyRegistered reports whether a strategy with the given ID is already
// present in the slice, so restart-survival registration does not duplicate it.
func sealStrategyRegistered(strategies []seal.SealingStrategy, id seal.StrategyID) bool {
	for _, s := range strategies {
		if s.ID() == id {
			return true
		}
	}
	return false
}
