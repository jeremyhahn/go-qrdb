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

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
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
	logger        *logger.Logger
	conn          *ConnectionBinding
	server        *EmbeddedServerBinding
	configBinding *ConfigBinding
}

// NewSealBinding creates a new SealBinding backed by the given
// server and connection bindings.
func NewSealBinding(log *logger.Logger, server *EmbeddedServerBinding, conn *ConnectionBinding) *SealBinding {
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
	if err := cl.Barrier().BarrierInitialize(context.Background(), &transport.BarrierInitializeRequest{
		Secret: secret,
	}); err != nil {
		b.logger.Error("Initialize failed", "error", err)
		return err
	}

	if b.configBinding != nil {
		b.configBinding.MarkInitialized()
	}

	b.logger.Debug("Initialize completed")
	return nil
}

// InitializeShamir initializes the encryption barrier using Shamir
// secret sharing. The response is share-free: shares are sealed to
// enrolled Security Officers and claimed via the officer ceremony, and
// the root token requires a post-claim share quorum
// (BarrierGenerateRootToken), so RootToken is always empty here.
func (b *SealBinding) InitializeShamir(secret string, threshold, totalShares int) (*BootstrapResult, error) {
	b.logger.Debug("InitializeShamir called", "threshold", threshold, "totalShares", totalShares)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("InitializeShamir failed", "error", err)
		return nil, err
	}
	resp, err := cl.Barrier().BarrierInitializeShamir(context.Background(), &transport.BarrierInitializeShamirRequest{
		Secret:      secret,
		Threshold:   threshold,
		TotalShares: totalShares,
	})
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

// Unseal submits unseal material toward unsealing the barrier. The
// barrier strategy drives the routing: Shamir barriers accept a single
// officer share toward the quorum threshold, while passphrase barriers
// unseal in one call with the master passphrase.
func (b *SealBinding) Unseal(share string) (*UnsealProgress, error) {
	b.logger.Debug("Unseal called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Unseal failed", "error", err)
		return nil, err
	}

	ctx := context.Background()
	status, err := cl.Barrier().BarrierStatus(ctx)
	if err != nil {
		b.logger.Error("Unseal failed", "error", err)
		return nil, err
	}

	if status.Strategy == "shamir" {
		resp, err := cl.Barrier().BarrierUnsealWithShare(ctx, &transport.BarrierUnsealShareRequest{
			Share: share,
		})
		if err != nil {
			b.logger.Error("Unseal failed", "error", err)
			return nil, err
		}
		b.logger.Debug("Unseal share accepted", "submitted", resp.Submitted, "required", resp.Required, "complete", resp.Complete)
		return &UnsealProgress{
			Sealed:         !resp.Complete,
			SharesProvided: resp.Submitted,
			SharesRequired: resp.Required,
		}, nil
	}

	if err := cl.Barrier().BarrierUnseal(ctx, &transport.BarrierUnsealRequest{
		Secret: share,
	}); err != nil {
		b.logger.Error("Unseal failed", "error", err)
		return nil, err
	}

	b.logger.Debug("Unseal completed, barrier unsealed")
	return &UnsealProgress{Sealed: false}, nil
}

// Seal seals the barrier, preventing further data access until it is
// unsealed again. Shamir barriers reject direct sealing — a quorum of
// officer seal votes (ShamirSealVote) is required — and that policy is
// enforced by the barrier service, surfacing here as an error.
func (b *SealBinding) Seal() error {
	b.logger.Debug("Seal called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Seal failed", "error", err)
		return err
	}
	if err := cl.Barrier().BarrierSeal(context.Background()); err != nil {
		b.logger.Error("Seal failed", "error", err)
		return err
	}
	b.logger.Debug("Seal completed")
	return nil
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
	resp, err := cl.Barrier().BarrierEnableEncryption(context.Background(), req)
	if err != nil {
		b.logger.Error("EnableEncryption failed", "error", err)
		return nil, err
	}
	b.logger.Debug("EnableEncryption completed", "status", resp.Status, "scope", resp.Scope)
	return &EnableEncryptionResult{Status: resp.Status, Scope: resp.Scope}, nil
}

// Status returns the current seal state of the barrier. Initialized is
// derived from the barrier's initialization timestamp; for Shamir
// barriers SharesRequired/SharesTotal carry the configured M-of-N
// quorum. SharesProvided is not tracked across status calls — quorum
// progress is reported per-submission by Unseal.
func (b *SealBinding) Status() (*SealStatus, error) {
	b.logger.Debug("Status called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Status failed", "error", err)
		return nil, err
	}
	resp, err := cl.Barrier().BarrierStatus(context.Background())
	if err != nil {
		b.logger.Error("Status failed", "error", err)
		return nil, err
	}

	status := &SealStatus{
		Sealed:         resp.Sealed,
		Initialized:    resp.InitializedAt != "",
		Strategy:       resp.Strategy,
		SharesRequired: resp.ShamirThreshold,
		SharesTotal:    resp.ShamirTotal,
	}
	b.logger.Debug("Status completed", "sealed", status.Sealed, "initialized", status.Initialized, "strategy", status.Strategy)
	return status, nil
}
