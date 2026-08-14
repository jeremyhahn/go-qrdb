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

// ---------------------------------------------------------------------------
// Wire-shape DTOs
//
// PEM material crosses the Wails IPC boundary as plain text, mirroring the
// REST wire contract (csr_pem / cert_pem are PEM strings, not base64). The
// transport types use []byte for these fields, which encoding/json would
// force into base64 — so registration ops use local DTOs and convert.
// Signature and share bytes are base64 on both wires, so the claim and
// seal-vote ops pass the transport types through unchanged.
// ---------------------------------------------------------------------------

// OfficerRegisterParams carries a Security Officer registration across the
// Wails IPC boundary. CSRPEM is plain PEM text.
type OfficerRegisterParams struct {
	Username          string `json:"username"`
	CSRPEM            string `json:"csr_pem"`
	AuthenticatorType string `json:"authenticator_type,omitempty"`
	AuthenticatorRef  string `json:"authenticator_ref,omitempty"`
}

// TenantOfficerRegisterParams carries a tenant-barrier Security Officer
// registration across the Wails IPC boundary. CSRPEM is plain PEM text.
type TenantOfficerRegisterParams struct {
	TenantID          string `json:"tenant_id"`
	Username          string `json:"username"`
	CSRPEM            string `json:"csr_pem"`
	AuthenticatorType string `json:"authenticator_type,omitempty"`
	AuthenticatorRef  string `json:"authenticator_ref,omitempty"`
}

// OfficerRegisterResult is the registration response returned to the
// frontend. CertPEM is plain PEM text.
type OfficerRegisterResult struct {
	CertPEM    string `json:"cert_pem"`
	ShareIndex int    `json:"share_index"`
}

// OfficerBinding exposes Security Officer enrollment, the per-SO share
// claim ceremony, and quorum seal voting to the Wails frontend for both
// the system barrier and per-tenant barriers.
type OfficerBinding struct {
	logger *logger.Logger
	conn   *ConnectionBinding
}

// NewOfficerBinding creates a new OfficerBinding backed by the given
// connection.
func NewOfficerBinding(log *logger.Logger, conn *ConnectionBinding) *OfficerBinding {
	return &OfficerBinding{logger: log, conn: conn}
}

// RegisterOfficer registers a Security Officer for the system barrier and
// returns the signed certificate PEM with the assigned share index.
func (b *OfficerBinding) RegisterOfficer(req *OfficerRegisterParams) (*OfficerRegisterResult, error) {
	b.logger.Debug("RegisterOfficer called", "username", req.Username)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("RegisterOfficer failed", "error", err)
		return nil, err
	}
	resp, err := cl.Barrier().OfficerRegister(context.Background(), &transport.OfficerRegisterRequest{
		Username:          req.Username,
		CSRPEM:            []byte(req.CSRPEM),
		AuthenticatorType: req.AuthenticatorType,
		AuthenticatorRef:  req.AuthenticatorRef,
	})
	if err != nil {
		b.logger.Error("RegisterOfficer failed", "username", req.Username, "error", err)
		return nil, err
	}
	b.logger.Debug("RegisterOfficer completed", "username", req.Username, "shareIndex", resp.ShareIndex)
	return &OfficerRegisterResult{CertPEM: string(resp.CertPEM), ShareIndex: resp.ShareIndex}, nil
}

// ListOfficers lists all registered Security Officers for the system
// barrier.
func (b *OfficerBinding) ListOfficers() ([]*transport.OfficerStatus, error) {
	b.logger.Debug("ListOfficers called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("ListOfficers failed", "error", err)
		return nil, err
	}
	officers, err := cl.Barrier().OfficerList(context.Background())
	if err != nil {
		b.logger.Error("ListOfficers failed", "error", err)
		return nil, err
	}
	b.logger.Debug("ListOfficers completed", "count", len(officers))
	return officers, nil
}

// OfficerClaimStatus returns the per-officer claim status for the system
// barrier (no share material).
func (b *OfficerBinding) OfficerClaimStatus() ([]*transport.OfficerStatus, error) {
	b.logger.Debug("OfficerClaimStatus called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("OfficerClaimStatus failed", "error", err)
		return nil, err
	}
	officers, err := cl.Barrier().OfficerClaimStatus(context.Background())
	if err != nil {
		b.logger.Error("OfficerClaimStatus failed", "error", err)
		return nil, err
	}
	b.logger.Debug("OfficerClaimStatus completed", "count", len(officers))
	return officers, nil
}

// ClaimBegin issues a challenge nonce to the named Security Officer.
func (b *OfficerBinding) ClaimBegin(req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	b.logger.Debug("ClaimBegin called", "username", req.Username)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("ClaimBegin failed", "error", err)
		return nil, err
	}
	resp, err := cl.Barrier().ShareClaimBegin(context.Background(), req)
	if err != nil {
		b.logger.Error("ClaimBegin failed", "username", req.Username, "error", err)
		return nil, err
	}
	b.logger.Debug("ClaimBegin completed", "username", req.Username)
	return resp, nil
}

// ClaimComplete verifies the officer's nonce signature and returns the
// share bytes exactly once. The frontend must wipe the share after use.
func (b *OfficerBinding) ClaimComplete(req *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	b.logger.Debug("ClaimComplete called", "username", req.Username)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("ClaimComplete failed", "error", err)
		return nil, err
	}
	resp, err := cl.Barrier().ShareClaimComplete(context.Background(), req)
	if err != nil {
		b.logger.Error("ClaimComplete failed", "username", req.Username, "error", err)
		return nil, err
	}
	b.logger.Debug("ClaimComplete completed", "username", req.Username, "shareIndex", resp.ShareIndex)
	return resp, nil
}

// RegisterRecoveryOfficer registers a Security Officer for the system
// barrier's recovery key set.
func (b *OfficerBinding) RegisterRecoveryOfficer(req *OfficerRegisterParams) (*OfficerRegisterResult, error) {
	b.logger.Debug("RegisterRecoveryOfficer called", "username", req.Username)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("RegisterRecoveryOfficer failed", "error", err)
		return nil, err
	}
	resp, err := cl.Barrier().RecoveryOfficerRegister(context.Background(), &transport.OfficerRegisterRequest{
		Username:          req.Username,
		CSRPEM:            []byte(req.CSRPEM),
		AuthenticatorType: req.AuthenticatorType,
		AuthenticatorRef:  req.AuthenticatorRef,
	})
	if err != nil {
		b.logger.Error("RegisterRecoveryOfficer failed", "username", req.Username, "error", err)
		return nil, err
	}
	b.logger.Debug("RegisterRecoveryOfficer completed", "username", req.Username, "shareIndex", resp.ShareIndex)
	return &OfficerRegisterResult{CertPEM: string(resp.CertPEM), ShareIndex: resp.ShareIndex}, nil
}

// RecoveryClaimBegin issues a challenge nonce for a recovery officer.
func (b *OfficerBinding) RecoveryClaimBegin(req *transport.ShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	b.logger.Debug("RecoveryClaimBegin called", "username", req.Username)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("RecoveryClaimBegin failed", "error", err)
		return nil, err
	}
	resp, err := cl.Barrier().RecoveryShareClaimBegin(context.Background(), req)
	if err != nil {
		b.logger.Error("RecoveryClaimBegin failed", "username", req.Username, "error", err)
		return nil, err
	}
	b.logger.Debug("RecoveryClaimBegin completed", "username", req.Username)
	return resp, nil
}

// RecoveryClaimComplete verifies the recovery officer's nonce signature and
// returns the recovery share bytes exactly once.
func (b *OfficerBinding) RecoveryClaimComplete(req *transport.ShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	b.logger.Debug("RecoveryClaimComplete called", "username", req.Username)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("RecoveryClaimComplete failed", "error", err)
		return nil, err
	}
	resp, err := cl.Barrier().RecoveryShareClaimComplete(context.Background(), req)
	if err != nil {
		b.logger.Error("RecoveryClaimComplete failed", "username", req.Username, "error", err)
		return nil, err
	}
	b.logger.Debug("RecoveryClaimComplete completed", "username", req.Username, "shareIndex", resp.ShareIndex)
	return resp, nil
}

// SubmitSealVote submits a Shamir share as a seal vote for the system
// barrier. When the threshold is met the barrier seals automatically.
func (b *OfficerBinding) SubmitSealVote(req *transport.ShamirSealVoteRequest) (*transport.ShamirSealVoteResponse, error) {
	b.logger.Debug("SubmitSealVote called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("SubmitSealVote failed", "error", err)
		return nil, err
	}
	resp, err := cl.Barrier().ShamirSealVote(context.Background(), req)
	if err != nil {
		b.logger.Error("SubmitSealVote failed", "error", err)
		return nil, err
	}
	b.logger.Debug("SubmitSealVote completed", "submitted", resp.Submitted, "required", resp.Required, "complete", resp.Complete)
	return resp, nil
}

// ListTenantOfficers lists all registered Security Officers for the given
// tenant's barrier.
func (b *OfficerBinding) ListTenantOfficers(tenantID string) ([]*transport.OfficerStatus, error) {
	b.logger.Debug("ListTenantOfficers called", "tenantID", tenantID)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("ListTenantOfficers failed", "error", err)
		return nil, err
	}
	officers, err := cl.Tenants().TenantBarrierOfficerList(context.Background(), tenantID)
	if err != nil {
		b.logger.Error("ListTenantOfficers failed", "tenantID", tenantID, "error", err)
		return nil, err
	}
	b.logger.Debug("ListTenantOfficers completed", "tenantID", tenantID, "count", len(officers))
	return officers, nil
}

// TenantOfficerClaimStatus returns per-officer claim status for the given
// tenant's barrier (no share material).
func (b *OfficerBinding) TenantOfficerClaimStatus(tenantID string) ([]*transport.OfficerStatus, error) {
	b.logger.Debug("TenantOfficerClaimStatus called", "tenantID", tenantID)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("TenantOfficerClaimStatus failed", "error", err)
		return nil, err
	}
	officers, err := cl.Tenants().TenantBarrierOfficerClaimStatus(context.Background(), tenantID)
	if err != nil {
		b.logger.Error("TenantOfficerClaimStatus failed", "tenantID", tenantID, "error", err)
		return nil, err
	}
	b.logger.Debug("TenantOfficerClaimStatus completed", "tenantID", tenantID, "count", len(officers))
	return officers, nil
}

// RegisterTenantOfficer registers a Security Officer for the given tenant's
// barrier and returns the signed certificate PEM with the share index.
func (b *OfficerBinding) RegisterTenantOfficer(req *TenantOfficerRegisterParams) (*OfficerRegisterResult, error) {
	b.logger.Debug("RegisterTenantOfficer called", "tenantID", req.TenantID, "username", req.Username)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("RegisterTenantOfficer failed", "error", err)
		return nil, err
	}
	resp, err := cl.Tenants().TenantBarrierOfficerRegister(context.Background(), &transport.TenantOfficerRegisterRequest{
		TenantID:          req.TenantID,
		Username:          req.Username,
		CSRPEM:            []byte(req.CSRPEM),
		AuthenticatorType: req.AuthenticatorType,
		AuthenticatorRef:  req.AuthenticatorRef,
	})
	if err != nil {
		b.logger.Error("RegisterTenantOfficer failed", "tenantID", req.TenantID, "username", req.Username, "error", err)
		return nil, err
	}
	b.logger.Debug("RegisterTenantOfficer completed", "tenantID", req.TenantID, "username", req.Username, "shareIndex", resp.ShareIndex)
	return &OfficerRegisterResult{CertPEM: string(resp.CertPEM), ShareIndex: resp.ShareIndex}, nil
}

// TenantClaimBegin issues a challenge nonce for a tenant barrier officer.
func (b *OfficerBinding) TenantClaimBegin(req *transport.TenantShareClaimBeginRequest) (*transport.ShareClaimBeginResponse, error) {
	b.logger.Debug("TenantClaimBegin called", "tenantID", req.TenantID, "username", req.Username)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("TenantClaimBegin failed", "error", err)
		return nil, err
	}
	resp, err := cl.Tenants().TenantBarrierShareClaimBegin(context.Background(), req)
	if err != nil {
		b.logger.Error("TenantClaimBegin failed", "tenantID", req.TenantID, "username", req.Username, "error", err)
		return nil, err
	}
	b.logger.Debug("TenantClaimBegin completed", "tenantID", req.TenantID, "username", req.Username)
	return resp, nil
}

// TenantClaimComplete verifies the tenant officer's nonce signature and
// returns the share bytes exactly once.
func (b *OfficerBinding) TenantClaimComplete(req *transport.TenantShareClaimCompleteRequest) (*transport.ShareClaimCompleteResponse, error) {
	b.logger.Debug("TenantClaimComplete called", "tenantID", req.TenantID, "username", req.Username)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("TenantClaimComplete failed", "error", err)
		return nil, err
	}
	resp, err := cl.Tenants().TenantBarrierShareClaimComplete(context.Background(), req)
	if err != nil {
		b.logger.Error("TenantClaimComplete failed", "tenantID", req.TenantID, "username", req.Username, "error", err)
		return nil, err
	}
	b.logger.Debug("TenantClaimComplete completed", "tenantID", req.TenantID, "username", req.Username, "shareIndex", resp.ShareIndex)
	return resp, nil
}

// SubmitTenantSealVote submits a Shamir share as a seal vote for the given
// tenant's barrier.
func (b *OfficerBinding) SubmitTenantSealVote(req *transport.TenantShamirSealVoteRequest) (*transport.ShamirSealVoteResponse, error) {
	b.logger.Debug("SubmitTenantSealVote called", "tenantID", req.TenantID)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("SubmitTenantSealVote failed", "error", err)
		return nil, err
	}
	resp, err := cl.Tenants().TenantBarrierShamirSealVote(context.Background(), req)
	if err != nil {
		b.logger.Error("SubmitTenantSealVote failed", "tenantID", req.TenantID, "error", err)
		return nil, err
	}
	b.logger.Debug("SubmitTenantSealVote completed", "tenantID", req.TenantID, "submitted", resp.Submitted, "required", resp.Required, "complete", resp.Complete)
	return resp, nil
}
