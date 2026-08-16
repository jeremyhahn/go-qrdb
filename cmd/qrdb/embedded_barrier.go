//go:build qrdb_embedded

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
	"errors"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// initEmbeddedBarrier creates an in-process seal barrier for embedded GUI
// mode so that all seal-gated services (tenant, ceremony, credential, cert)
// are available without requiring an operator ceremony.
//
// Unlike earlier revisions, the barrier is backed by a PERSISTENT on-disk
// pebble seal store at filepath.Join(dataDir, "seal") and NEVER generates a
// random ephemeral key. The sealing strategy is selected from the operator's
// SealConfig:
//
//   - nil / "" / "none" / "disabled": seal.NewNoneStrategy — plaintext at
//     rest, no key material, no ceremony. The encrypting FS is a pass-through,
//     so the persistent data-plane pebble store decrypts cleanly across
//     restarts. This is the default.
//   - "shamir": seal.NewShamirStrategy(SharesRequired, SharesTotal).
//   - "passphrase": seal.NewDeferredSoftwareStrategy.
//
// For the None strategy the barrier is auto-Initialized on a fresh data dir
// and auto-Unsealed on a restart (both with empty credentials, effectively
// no-ops). Passphrase and Shamir barriers are constructed but left
// uninitialized (fresh) or sealed (restart): the GUI drives Initialize /
// InitializeShamir / Unseal through the real BarrierService, so credentials
// flow only from the operator and shares only through the officer claim
// ceremony (INV-1).
//
// Returns the barrier, registry, credential service, ceremony data dir, and
// any initialization error. On error all returned pointers are nil and the
// caller should log a warning and proceed without seal services.
func initEmbeddedBarrier(dataDir string, sealCfg *SealConfig) (
	barrier *seal.StorageBarrier,
	registry *seal.BarrierRegistry,
	cred *credentials.CredentialService,
	ceremonyDataDir string,
	err error,
) {
	// Persistent on-disk seal store. Mirrors the layout used by the SDK
	// embedded server (sdk/go/transport/embedded/server.go::setupBarrier).
	sealDir := filepath.Join(dataDir, "seal")
	// SyncWrites is forced on: this store is the sole persisted copy of the
	// root-key blob and epoch/rotation state, not a Raft-replicated store,
	// so an unsynced write can be lost on crash even though it appeared to
	// succeed — orphaning every file the lost root key had encrypted.
	sealStoreCfg := pebbleStorage.DefaultConfig(sealDir)
	sealStoreCfg.SyncWrites = true
	sealStore, storeErr := pebbleStorage.New(sealStoreCfg)
	if storeErr != nil {
		return nil, nil, nil, "", &EmbeddedBarrierError{Step: "sealstore", Err: storeErr}
	}
	sealBackend := seal.NewStorageAdapter(sealStore)

	barrierCfg := seal.BarrierConfig{
		RootKeyPath: "root-key",
		// DeploymentID 0 is acceptable for single-process embedded mode.
	}

	// Select the sealing strategy from the operator's SealConfig. Default to
	// None (plaintext at rest) when no seal config is supplied. A random key
	// is NEVER generated.
	mode := ""
	if sealCfg != nil {
		mode = strings.ToLower(strings.TrimSpace(sealCfg.Mode))
	}

	var strategy seal.SealingStrategy
	switch mode {
	case "", "none", "disabled":
		strategy = seal.NewNoneStrategy()

	case "shamir":
		// The strategy is constructed from the configured M-of-N; the
		// barrier is NOT initialized or unsealed here. Initialization and
		// quorum unsealing are driven through the real BarrierService by
		// the GUI (SealBinding) using the share-free officer-claim flow.
		shamirStrat, stratErr := seal.NewShamirStrategy(sealCfg.SharesRequired, sealCfg.SharesTotal)
		if stratErr != nil {
			_ = sealStore.Close()
			return nil, nil, nil, "", &EmbeddedBarrierError{Step: "strategy", Err: stratErr}
		}
		barrierCfg.Shamir = &seal.ShamirConfig{
			Threshold:   sealCfg.SharesRequired,
			TotalShares: sealCfg.SharesTotal,
		}
		strategy = shamirStrat

	case "passphrase":
		// The deferred software strategy extracts the passphrase from
		// Credentials.Secret on each seal/unseal call. The barrier is NOT
		// initialized here; the GUI (SealBinding) drives Initialize/Unseal
		// through the real BarrierService with the operator's passphrase.
		// No passphrase is ever fabricated or persisted.
		strategy = seal.NewDeferredSoftwareStrategy()

	default:
		_ = sealStore.Close()
		return nil, nil, nil, "", &EmbeddedBarrierError{
			Step: "strategy",
			Err:  errors.New("unknown seal mode: " + mode),
		}
	}

	// Explicit strategy selection (no implicit preference-order default).
	barrierCfg.PreferenceOrder = []seal.StrategyID{strategy.ID()}

	b, barrierErr := seal.NewStorageBarrier(slog.Default(), sealBackend, barrierCfg, strategy)
	if barrierErr != nil {
		_ = sealStore.Close()
		return nil, nil, nil, "", &EmbeddedBarrierError{Step: "barrier", Err: barrierErr}
	}

	// Auto init-or-unseal applies ONLY to the credential-free None strategy:
	// on a fresh seal store Initialize mints the root key and leaves the
	// barrier unsealed; on a restart Initialize returns ErrAlreadyInitialized,
	// in which case we Unseal the existing envelope. Both paths use empty
	// credentials and are effectively no-ops, mirroring runAutoUnseal's
	// default branch in the SDK embedded server.
	//
	// Passphrase and Shamir barriers are intentionally left untouched here —
	// fresh stores stay uninitialized and restarted stores stay sealed. The
	// GUI drives Initialize / InitializeShamir / Unseal through the real
	// BarrierService (daemon sealed-window semantics): operations on a
	// sealed barrier fail at call time until the operator supplies the
	// passphrase or the officer quorum submits its shares.
	if mode == "" || mode == "none" || mode == "disabled" {
		ctx := context.Background()
		creds := seal.Credentials{}
		if initErr := b.Initialize(ctx, creds); initErr != nil {
			if !errors.Is(initErr, seal.ErrAlreadyInitialized) {
				_ = sealStore.Close()
				return nil, nil, nil, "", &EmbeddedBarrierError{Step: "initialize", Err: initErr}
			}
			if unsealErr := b.Unseal(ctx, creds); unsealErr != nil && !errors.Is(unsealErr, seal.ErrAlreadyUnsealed) {
				_ = sealStore.Close()
				return nil, nil, nil, "", &EmbeddedBarrierError{Step: "unseal", Err: unsealErr}
			}
		}
	}

	reg, regErr := seal.NewBarrierRegistry(slog.Default(), b)
	if regErr != nil {
		_ = sealStore.Close()
		return nil, nil, nil, "", &EmbeddedBarrierError{Step: "registry", Err: regErr}
	}

	credSvc, credErr := credentials.NewCredentialService(
		&credentials.Config{Strategy: "barrier"}, b, slog.Default())
	if credErr != nil {
		_ = sealStore.Close()
		return nil, nil, nil, "", &EmbeddedBarrierError{Step: "credential", Err: credErr}
	}

	ceremonyDir := filepath.Join(dataDir, "ceremony")
	return b, reg, credSvc, ceremonyDir, nil
}
