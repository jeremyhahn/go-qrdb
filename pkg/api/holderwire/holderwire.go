// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package holderwire converts the share-holder messages of the SDK's
// transport vocabulary (sdk/go/transport) to and from the service layer. The
// REST server, the MCP server and the embedded transport carry those types
// directly, so they share these conversions and a holder message means the
// same thing on each; the protobuf-carrying transports use holderpb.
package holderwire

import (
	"bytes"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// FactorsFrom converts factor enrolments.
func FactorsFrom(in []transport.HolderFactorEnrolment) []ceremony.FactorEnrolment {
	if len(in) == 0 {
		return nil
	}
	out := make([]ceremony.FactorEnrolment, len(in))
	for i, f := range in {
		out[i] = ceremony.FactorEnrolment{Name: f.Name, Params: f.Params}
	}
	return out
}

// RegisterFrom converts a system-barrier holder registration.
func RegisterFrom(req *transport.OfficerRegisterRequest) *service.BarrierOfficerRegisterRequest {
	return &service.BarrierOfficerRegisterRequest{
		Username:      req.Username,
		Role:          seal.HolderRole(req.Role),
		CSRPEM:        []byte(req.CSRPEM),
		EncryptionKEM: req.EncryptionKEM,
		EncryptionKey: req.EncryptionKey,
		Factors:       FactorsFrom(req.Factors),
		Designated:    req.Designated,
	}
}

// TenantBarrierRosterFrom converts the inline Shamir officer roster a
// `tenant barrier-init` wire request carries into the []ceremony.OfficerConfig
// shape ScopedCeremonyService.InitializeShamirWithRoster validates and
// registers atomically with initialization -- the SAME conversion for every
// transport that accepts this inline roster (REST, QUIC, MCP, Embedded), so
// there is exactly one place it can ever drift. Returns nil when officers is
// empty.
func TenantBarrierRosterFrom(officers []transport.OfficerRegisterRequest) []ceremony.OfficerConfig {
	if len(officers) == 0 {
		return nil
	}
	out := make([]ceremony.OfficerConfig, len(officers))
	for i := range officers {
		out[i] = ceremony.OfficerConfig{
			Username:      officers[i].Username,
			Role:          seal.HolderRole(officers[i].Role),
			CSRPEM:        []byte(officers[i].CSRPEM),
			EncryptionKEM: officers[i].EncryptionKEM,
			EncryptionKey: officers[i].EncryptionKey,
			Factors:       FactorsFrom(officers[i].Factors),
			Designated:    officers[i].Designated,
		}
	}
	return out
}

// TenantBarrierHoldersTo converts the per-officer certificates a tenant
// barrier's inline-roster initialization (tenant.TenantBarrierInitResult.Holders)
// returns into the wire shape TenantBarrierInitResponse carries -- the SAME
// conversion for every transport that returns this result (REST, QUIC, MCP,
// Embedded).
func TenantBarrierHoldersTo(holders []tenant.TenantBarrierHolderCert) []transport.TenantBarrierHolderCert {
	if len(holders) == 0 {
		return nil
	}
	out := make([]transport.TenantBarrierHolderCert, len(holders))
	for i, h := range holders {
		out[i] = transport.TenantBarrierHolderCert{
			Username: h.Username,
			Role:     h.Role,
			CertPEM:  h.CertPEM,
		}
	}
	return out
}

// TenantRegisterFrom converts a tenant-barrier holder registration.
func TenantRegisterFrom(req *transport.TenantOfficerRegisterRequest) *service.BarrierOfficerRegisterRequest {
	return &service.BarrierOfficerRegisterRequest{
		Username:      req.Username,
		Role:          seal.HolderRole(req.Role),
		CSRPEM:        []byte(req.CSRPEM),
		EncryptionKEM: req.EncryptionKEM,
		EncryptionKey: req.EncryptionKey,
		Factors:       FactorsFrom(req.Factors),
		Designated:    req.Designated,
	}
}

// RegisterTo converts a system-barrier registration result.
func RegisterTo(r *service.BarrierOfficerRegisterResult) *transport.OfficerRegisterResponse {
	return &transport.OfficerRegisterResponse{
		CertPEM:       string(r.CertPEM),
		Role:          string(r.Role),
		Designated:    r.Designated,
		EncryptionKEM: r.EncryptionKEM,
		Factors:       r.Factors,
	}
}

// TenantRegisterTo converts a tenant-barrier registration result.
func TenantRegisterTo(r *ceremony.RegisterOfficerResult) *transport.OfficerRegisterResponse {
	return &transport.OfficerRegisterResponse{
		CertPEM:       string(r.CertPEM),
		Role:          string(r.Role),
		Designated:    r.Designated,
		EncryptionKEM: r.EncryptionKEM,
		Factors:       r.Factors,
	}
}

// StatusesTo converts holder status snapshots.
func StatusesTo(officers []*ceremony.OfficerStatus) []*transport.OfficerStatus {
	out := make([]*transport.OfficerStatus, len(officers))
	for i, o := range officers {
		out[i] = &transport.OfficerStatus{
			Username:       o.Username,
			Role:           string(o.Role),
			CertThumbprint: o.CertThumbprint,
			ShareIndex:     o.ShareIndex,
			Designated:     o.Designated,
			Factors:        o.Factors,
			EncryptionKEM:  o.EncryptionKEM,
			EnrolledAt:     o.EnrolledAt,
			ClaimedAt:      o.ClaimedAt,
			SharePending:   o.SharePending,
		}
	}
	return out
}

// ChallengeTo converts a quorum challenge.
func ChallengeTo(c *ceremony.QuorumChallenge) *transport.QuorumChallenge {
	return &transport.QuorumChallenge{
		ChallengeID: c.ChallengeID,
		Domain:      string(c.Domain),
		Op:          string(c.Op),
		Username:    c.Holder,
		Factor:      c.Factor,
		Nonce:       c.Nonce,
		MethodData:  c.MethodData,
		ExpiresAt:   c.ExpiresAt,
	}
}

// BeginFrom converts a quorum challenge request.
func BeginFrom(req *transport.QuorumBeginRequest) *service.QuorumBeginRequest {
	return &service.QuorumBeginRequest{
		Domain:   seal.QuorumDomain(req.Domain),
		Op:       seal.QuorumOp(req.Op),
		Username: req.Username,
		Factor:   req.Factor,
	}
}

// SubmitFrom converts a quorum submission. peerThumbprint is the verified
// TLS client certificate the TRANSPORT observed ("" when none); it is never
// read from the request.
func SubmitFrom(req *transport.QuorumSubmitRequest, peerThumbprint string) *service.QuorumSubmitRequest {
	return &service.QuorumSubmitRequest{
		Domain:             seal.QuorumDomain(req.Domain),
		Op:                 seal.QuorumOp(req.Op),
		Username:           req.Username,
		ChallengeID:        req.ChallengeID,
		Factor:             req.Factor,
		Response:           req.Response,
		ResponseMetadata:   req.ResponseMetadata,
		PeerCertThumbprint: peerThumbprint,
		Share:              req.Share,
	}
}

// TenantSubmitFrom converts a tenant quorum submission; see SubmitFrom. A
// tenant barrier has only the unseal domain.
func TenantSubmitFrom(req *transport.TenantQuorumSubmitRequest, peerThumbprint string) *seal.HolderSubmission {
	return &seal.HolderSubmission{
		Domain: seal.DomainUnseal,
		Op:     seal.QuorumOp(req.Op),
		Holder: req.Username,
		Proof: seal.HolderProof{
			ChallengeID:        req.ChallengeID,
			Factor:             req.Factor,
			Response:           req.Response,
			ResponseMetadata:   req.ResponseMetadata,
			PeerCertThumbprint: peerThumbprint,
		},
		Share: req.Share,
	}
}

// OutcomeTo converts a submission outcome.
func OutcomeTo(o *service.QuorumOutcomeInfo) *transport.QuorumOutcome {
	out := &transport.QuorumOutcome{
		Domain:    string(o.Domain),
		Op:        string(o.Op),
		Required:  o.Required,
		Submitted: o.Submitted,
		Complete:  o.Complete,
	}
	if o.RootToken != nil {
		out.RootToken = o.RootToken.Token
		out.RootTokenCreatedAt = o.RootToken.CreatedAt
	}
	if o.Rekey != nil {
		out.Threshold = o.Rekey.Threshold
		out.TotalShares = o.Rekey.TotalShares
		out.ShareCount = o.Rekey.ShareCount
	}
	return out
}

// RoundTo converts an open round.
func RoundTo(r *seal.QuorumRoundInfo) *transport.QuorumRound {
	return &transport.QuorumRound{
		Domain:       string(r.Domain),
		Op:           string(r.Op),
		Required:     r.Required,
		Submitted:    r.Submitted,
		ExpiresAt:    r.ExpiresAt,
		NewThreshold: r.NewThreshold,
		Policy:       string(r.Policy),
		Remove:       r.Remove,
	}
}

// CeremonyInitFrom converts a ceremony initialization request. An empty holder
// list stays nil, so the service's "non-empty overrides the configured value"
// precedence applies unchanged.
//
// req.Secret, when non-empty, is consumed into a *secure.Buffer (secure
// analyzer rule 3: service.CeremonyInitializeOptions.Secret is protected
// memory, not a wire []byte) -- secure.NewBufferFromBytes wipes req.Secret's
// backing array as a side effect, so a caller no longer needs its own
// defer secure.Wipe(req.Secret). The returned Buffer is BORROWED by
// CeremonyInitialize exactly as the field it populates documents; the caller
// of CeremonyInitFrom owns it and must Destroy it once the ceremony call
// returns.
func CeremonyInitFrom(req *transport.CeremonyInitializeRequest) (*service.CeremonyInitializeOptions, error) {
	var holders []service.CeremonyOfficerConfig
	if len(req.Officers) > 0 {
		holders = make([]service.CeremonyOfficerConfig, len(req.Officers))
		for i, o := range req.Officers {
			holders[i] = service.CeremonyOfficerConfig{
				Username:      o.Username,
				Role:          seal.HolderRole(o.Role),
				CSRPEM:        o.CSRPEM,
				EncryptionKEM: o.EncryptionKEM,
				EncryptionKey: o.EncryptionKey,
				Factors:       FactorsFrom(o.Factors),
				Designated:    o.Designated,
			}
		}
	}
	var secretBuf *secure.Buffer
	if len(req.Secret) > 0 {
		var err error
		secretBuf, err = secure.NewBufferFromBytes(req.Secret)
		if err != nil {
			return nil, err
		}
	}
	return &service.CeremonyInitializeOptions{
		SOPin:                  req.SOPin,
		UserPin:                req.UserPin,
		CredentialSealStrategy: req.CredentialSealStrategy,
		Threshold:              req.Threshold,
		Policy:                 seal.FactorPolicy(req.Policy),
		Officers:               holders,
		Hostname:               req.Hostname,
		SANs:                   req.SANs,
		AdminCSRPEM:            req.AdminCSRPEM,
		AdminCertPEM:           req.AdminCertPEM,
		AdminCertPoolPEM:       req.AdminCertPoolPEM,
		AdminGenerate:          req.AdminGenerate,
		Secret:                 secretBuf,
	}, nil
}

// CeremonyInitTo converts a ceremony initialization result, including the
// certificate the CA issued each initial holder.
//
// AdminKeyPEM is an explicit bytes.Clone of r.AdminKeyPEM, not a shared
// slice (go-qrdb#560 review item 7, follow-up: the field is now []byte,
// not string, so unlike before it no longer makes its own independent
// copy by construction). Every transport that calls this (REST, MCP,
// embedded) wipes r.AdminKeyPEM (CeremonyInitResult.WipeAdminKeyPEM) once
// it has built its outgoing response -- sharing the backing array would
// zero the response's own copy, still unmarshaled/unread, out from under
// it. The clone is the one, unavoidable copy this value's single delivery
// to its caller requires; nothing beyond this call and the one wipe
// retains another.
func CeremonyInitTo(r *service.CeremonyInitResult) *transport.CeremonyInitializeResponse {
	holders := make([]transport.CeremonyHolderCert, len(r.Holders))
	for i, h := range r.Holders {
		holders[i] = transport.CeremonyHolderCert{Username: h.Username, Role: string(h.Role), CertPEM: string(h.CertPEM)}
	}
	return &transport.CeremonyInitializeResponse{
		SPKIPin:               r.SPKIPin,
		CACertPEM:             string(r.CACertPEM),
		TLSCertPEM:            string(r.TLSCertPEM),
		State:                 r.State,
		ShareCount:            r.ShareCount,
		Holders:               holders,
		AdminCertPEM:          string(r.AdminCertPEM),
		AdminKeyPEM:           bytes.Clone(r.AdminKeyPEM),
		AdminCredentialSource: r.AdminCredentialSource,
	}
}
