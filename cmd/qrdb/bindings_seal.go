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
	"bytes"
	"context"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/secure"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// SealBinding exposes barrier seal operations to the desktop GUI by
// delegating to the SDK BarrierClient on the active connection. It holds
// no seal state of its own: the embedded barrier (or the remote daemon)
// is the single source of truth, and share material is never generated,
// stored, or returned here (INV-1). Shamir shares are sealed to enrolled
// Security Officers at initialization and released only through the
// officer claim ceremony.
type SealBinding struct {
	logger        *sdk.ConcreteLogger
	conn          *ConnectionBinding
	server        *EmbeddedServerBinding
	configBinding *ConfigBinding
}

// NewSealBinding creates a new SealBinding backed by the given
// server and connection bindings.
func NewSealBinding(log *sdk.ConcreteLogger, server *EmbeddedServerBinding, conn *ConnectionBinding) *SealBinding {
	return &SealBinding{
		logger: log,
		server: server,
		conn:   conn,
	}
}

// SetConfigBinding wires the ConfigBinding so that Initialize and
// InitializeShamir can mark the server as configured.
func (b *SealBinding) SetConfigBinding(cb *ConfigBinding) {
	b.configBinding = cb
}

// Initialize initializes the encryption barrier with a passphrase.
// For "none" barrier mode, pass an empty string. On success the server
// is marked as configured.
func (b *SealBinding) Initialize(secret string) error {
	b.logger.Debug("Initialize called", "hasSecret", secret != "")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Initialize failed", "error", err)
		return err
	}
	req := &transport.BarrierInitializeRequest{Secret: secretBytes(secret)}
	defer secure.Wipe(req.Secret)
	if err := cl.Barrier().BarrierInitialize(context.Background(), req); err != nil {
		b.logger.Error("Initialize failed", "error", err)
		return err
	}

	if b.configBinding != nil {
		b.configBinding.MarkInitialized()
	}

	b.logger.Debug("Initialize completed")
	return nil
}

// InitializeShamir initializes the encryption barrier under Shamir custody,
// splitting the unseal key threshold-of-totalShares to the enrolled share
// holders under the given factor policy ("designated" or "any-enrolled";
// empty selects designated). The response is share-free: each share is
// sealed to its holder and claimed through the holder claim flow, and a root
// token needs a root-token quorum of holders, so RootToken is always empty
// here.
func (b *SealBinding) InitializeShamir(threshold, totalShares int, policy string) (*BootstrapResult, error) {
	b.logger.Debug("InitializeShamir called", "threshold", threshold, "totalShares", totalShares)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("InitializeShamir failed", "error", err)
		return nil, err
	}
	req := &transport.BarrierInitializeShamirRequest{
		Threshold:   threshold,
		TotalShares: totalShares,
		Policy:      policy,
	}
	resp, err := cl.Barrier().BarrierInitializeShamir(context.Background(), req)
	if err != nil {
		b.logger.Error("InitializeShamir failed", "error", err)
		return nil, err
	}

	if b.configBinding != nil {
		b.configBinding.MarkInitialized()
	}

	b.logger.Debug("InitializeShamir completed", "threshold", resp.Threshold, "shareCount", resp.ShareCount)
	return &BootstrapResult{
		RootToken:  "",
		Threshold:  resp.Threshold,
		ShareCount: resp.ShareCount,
	}, nil
}

// Unseal unseals a passphrase barrier with its master passphrase. It asks
// nothing first: a sealed node answers the unseal call and nothing else. The
// server refuses a Shamir barrier itself (seal.ErrShamirUnsealRequiresQuorum):
// its shares are submitted one authenticated holder at a time through
// OfficerBinding.SubmitShare.
func (b *SealBinding) Unseal(secret string) (*UnsealProgress, error) {
	b.logger.Debug("Unseal called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Unseal failed", "error", err)
		return nil, err
	}

	unsealReq := &transport.BarrierUnsealRequest{Secret: secretBytes(secret)}
	defer secure.Wipe(unsealReq.Secret)
	if err := cl.Barrier().BarrierUnseal(context.Background(), unsealReq); err != nil {
		b.logger.Error("Unseal failed", "error", err)
		return nil, err
	}

	b.logger.Debug("Unseal completed, barrier unsealed")
	return &UnsealProgress{Sealed: false}, nil
}

// Seal seals the barrier, preventing further data access until it is
// unsealed again. soPin is the Security Officer PIN, required once one is
// provisioned and empty otherwise; it rides the call's context
// (transport.WithSOPIN), which is where every network transport reads it.
// Shamir barriers reject direct sealing — a holder quorum submitting to the
// seal op is required — and that policy is enforced by the barrier service,
// surfacing here as an error.
func (b *SealBinding) Seal(soPin string) error {
	b.logger.Debug("Seal called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Seal failed", "error", err)
		return err
	}
	ctx, release, err := transport.WithSOPIN(context.Background(), []byte(soPin))
	if err != nil {
		b.logger.Error("Seal failed", "error", err)
		return err
	}
	defer release()
	if err := cl.Barrier().BarrierSeal(ctx); err != nil {
		b.logger.Error("Seal failed", "error", err)
		return err
	}
	b.logger.Debug("Seal completed")
	return nil
}

// ClusterUnseal clears the node barrier's cluster seal record once this node
// has unsealed with its own credential, so every node that is held serves
// again. It refuses while cluster sealing is not active, and while this
// node's own barrier is still sealed.
func (b *SealBinding) ClusterUnseal() (*transport.ClusterSeal, error) {
	b.logger.Debug("ClusterUnseal called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("ClusterUnseal failed", "error", err)
		return nil, err
	}
	cs, err := cl.Barrier().BarrierClusterUnseal(context.Background())
	if err != nil {
		b.logger.Error("ClusterUnseal failed", "error", err)
		return nil, err
	}
	b.logger.Debug("ClusterUnseal completed", "epoch", cs.Epoch)
	return cs, nil
}

// EnableEncryption migrates a previously-unencrypted ("none") barrier to a
// keyed strategy, re-encrypting all at-rest data. An empty TenantID targets the
// system barrier (admin scope); a set TenantID targets that tenant's dedicated
// barrier. The migration runs asynchronously on the server, so the GUI polls
// Status until the strategy changes from "none" to the keyed strategy.
func (b *SealBinding) EnableEncryption(req *transport.BarrierEnableEncryptionRequest) (*EnableEncryptionResult, error) {
	b.logger.Debug("EnableEncryption called", "tenantID", req.TenantID, "strategy", req.Strategy)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("EnableEncryption failed", "error", err)
		return nil, err
	}
	// req belongs to the CALLER. The Wails bridge hands this pointer in and the
	// frontend still holds it after the promise settles, so the passphrase
	// inside it is not ours to destroy -- wiping it here would hand the GUI back
	// a zeroed field of its own request object.
	//
	// Every other method on this binding takes a string and runs it through
	// secretBytes, which allocates; that is why they can wipe what they pass.
	// This one receives the struct already built, so the copy has to be made
	// here. We own `call.Passphrase` and nothing else.
	call := *req
	call.Passphrase = bytes.Clone(req.Passphrase)
	defer secure.Wipe(call.Passphrase)

	resp, err := cl.Barrier().BarrierEnableEncryption(context.Background(), &call)
	if err != nil {
		b.logger.Error("EnableEncryption failed", "error", err)
		return nil, err
	}
	b.logger.Debug("EnableEncryption completed", "status", resp.Status, "scope", resp.Scope)
	return &EnableEncryptionResult{Status: resp.Status, Scope: resp.Scope}, nil
}

// Status returns the current seal state of the barrier. Initialized is the
// node's own initialized flag, which a sealed node serves on its sealed
// surface, so a node awaiting its first boot reads as needing init; for
// Shamir barriers SharesRequired/SharesTotal carry the configured M-of-N
// quorum. SharesProvided is not tracked across status calls — quorum
// progress is reported per-submission by Unseal.
//
// A sealed node reports nothing beyond Sealed and Initialized until unseal.
// A node the cluster seal holds or cuts off refuses the call with
// *transport.BarrierSealedError, and that refusal is the answer: its own
// barrier is initialized, and it serves nothing until the cluster releases
// it.
func (b *SealBinding) Status() (*SealStatus, error) {
	b.logger.Debug("Status called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Status failed", "error", err)
		return nil, err
	}
	resp, err := cl.Barrier().BarrierStatus(context.Background())
	if transport.IsBarrierSealed(err) {
		b.logger.Debug("Status completed: the cluster seal holds the node, so it is initialized and serves nothing")
		return &SealStatus{Sealed: true, Initialized: true}, nil
	}
	if err != nil {
		b.logger.Error("Status failed", "error", err)
		return nil, err
	}

	status := &SealStatus{
		Sealed:           resp.Sealed,
		Initialized:      resp.Initialized,
		Strategy:         resp.Strategy,
		SharesRequired:   resp.ShamirThreshold,
		SharesTotal:      resp.ShamirTotal,
		ClusterSeal:      resp.ClusterSeal,
		ClusterMigration: resp.ClusterMigration,
	}
	b.logger.Debug("Status completed", "sealed", status.Sealed, "initialized", status.Initialized, "strategy", status.Strategy)
	return status, nil
}
