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

// Package holderpb converts the share-holder protobuf messages to and from
// the service layer. The gRPC, Unix and QUIC servers and every protobuf SDK
// client share these conversions, so a holder message means the same thing
// on every protobuf-carrying transport.
package holderpb

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// FactorParamsError is returned when a factor enrolment's params_json is not
// a JSON object.
type FactorParamsError struct {
	Factor string
	Err    error
}

// Error returns a human-readable description.
func (e *FactorParamsError) Error() string {
	return "holderpb: factor " + e.Factor + " params_json is not a JSON object: " + e.Err.Error()
}

// Unwrap returns the decode error.
func (e *FactorParamsError) Unwrap() error { return e.Err }

// ErrorClass reports a malformed request: the caller's params_json.
func (e *FactorParamsError) ErrorClass() errclass.Class { return errclass.InvalidArgument }

var _ errclass.Classed = (*FactorParamsError)(nil)

// FactorsFromPB decodes the additional factor enrolments.
func FactorsFromPB(in []*pb.HolderFactorEnrolment) ([]ceremony.FactorEnrolment, error) {
	out := make([]ceremony.FactorEnrolment, 0, len(in))
	for _, f := range in {
		params := map[string]any{}
		if raw := f.GetParamsJson(); len(raw) > 0 {
			if err := json.Unmarshal(raw, &params); err != nil {
				return nil, &FactorParamsError{Factor: f.GetName(), Err: err}
			}
		}
		out = append(out, ceremony.FactorEnrolment{Name: f.GetName(), Params: params})
	}
	return out, nil
}

// RegisterFromPB converts a registration request.
func RegisterFromPB(req *pb.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterRequest, error) {
	factors, err := FactorsFromPB(req.GetFactors())
	if err != nil {
		return nil, err
	}
	return &service.BarrierOfficerRegisterRequest{
		Username:      req.GetUsername(),
		Role:          seal.HolderRole(req.GetRole()),
		CSRPEM:        req.GetCsrPem(),
		EncryptionKEM: req.GetEncryptionKem(),
		EncryptionKey: req.GetEncryptionKey(),
		Factors:       factors,
		Designated:    req.GetDesignated(),
	}, nil
}

// RegisterToPB converts a registration result.
func RegisterToPB(r *service.BarrierOfficerRegisterResult) *pb.BarrierOfficerRegisterResponse {
	return &pb.BarrierOfficerRegisterResponse{
		CertPem:       r.CertPEM,
		Role:          string(r.Role),
		Designated:    r.Designated,
		EncryptionKem: r.EncryptionKEM,
		Factors:       r.Factors,
	}
}

// StatusesToPB converts holder status snapshots.
func StatusesToPB(officers []*ceremony.OfficerStatus) []*pb.OfficerStatus {
	out := make([]*pb.OfficerStatus, len(officers))
	for i, o := range officers {
		var claimedAt string
		if !o.ClaimedAt.IsZero() {
			claimedAt = o.ClaimedAt.Format(time.RFC3339)
		}
		out[i] = &pb.OfficerStatus{
			Username:       o.Username,
			CertThumbprint: o.CertThumbprint,
			ShareIndex:     int32(o.ShareIndex), //nolint:gosec
			EnrolledAt:     o.EnrolledAt.Format(time.RFC3339),
			ClaimedAt:      claimedAt,
			SharePending:   o.SharePending,
			Role:           string(o.Role),
			Designated:     o.Designated,
			Factors:        o.Factors,
			EncryptionKem:  o.EncryptionKEM,
		}
	}
	return out
}

// ChallengeToPB converts a quorum challenge.
func ChallengeToPB(c *ceremony.QuorumChallenge) *pb.BarrierQuorumChallenge {
	return &pb.BarrierQuorumChallenge{
		ChallengeId: c.ChallengeID,
		Domain:      string(c.Domain),
		Op:          string(c.Op),
		Username:    c.Holder,
		Factor:      c.Factor,
		Nonce:       c.Nonce,
		MethodData:  c.MethodData,
		ExpiresAt:   c.ExpiresAt.Format(time.RFC3339),
	}
}

// OutcomeToPB converts a submission outcome.
func OutcomeToPB(o *service.QuorumOutcomeInfo) *pb.BarrierQuorumOutcome {
	out := &pb.BarrierQuorumOutcome{
		Domain:    string(o.Domain),
		Op:        string(o.Op),
		Required:  int32(o.Required),  //nolint:gosec
		Submitted: int32(o.Submitted), //nolint:gosec
		Complete:  o.Complete,
	}
	if o.RootToken != nil {
		out.RootToken = o.RootToken.Token
		out.RootTokenCreatedAt = o.RootToken.CreatedAt.Format(time.RFC3339Nano)
	}
	if o.Rekey != nil {
		out.Threshold = int32(o.Rekey.Threshold)     //nolint:gosec
		out.TotalShares = int32(o.Rekey.TotalShares) //nolint:gosec
		out.ShareCount = int32(o.Rekey.ShareCount)   //nolint:gosec
	}
	return out
}

// RoundToPB converts an open round.
func RoundToPB(r *seal.QuorumRoundInfo) *pb.BarrierQuorumRound {
	return &pb.BarrierQuorumRound{
		Domain:       string(r.Domain),
		Op:           string(r.Op),
		Required:     int32(r.Required),  //nolint:gosec
		Submitted:    int32(r.Submitted), //nolint:gosec
		ExpiresAt:    r.ExpiresAt.Format(time.RFC3339),
		NewThreshold: int32(r.NewThreshold), //nolint:gosec
		Policy:       string(r.Policy),
		Remove:       r.Remove,
	}
}

// SubmitFromPB converts a quorum submission. peerThumbprint is the verified
// TLS client certificate the TRANSPORT observed; it is never read from the
// request.
func SubmitFromPB(req *pb.BarrierQuorumSubmitRequest, peerThumbprint string) *service.QuorumSubmitRequest {
	return &service.QuorumSubmitRequest{
		Domain:             seal.QuorumDomain(req.GetDomain()),
		Op:                 seal.QuorumOp(req.GetOp()),
		Username:           req.GetUsername(),
		ChallengeID:        req.GetChallengeId(),
		Factor:             req.GetFactor(),
		Response:           req.GetResponse(),
		ResponseMetadata:   req.GetResponseMetadata(),
		PeerCertThumbprint: peerThumbprint,
		Share:              req.GetShare(),
	}
}

// TenantSubmitFromPB converts a tenant quorum submission; see SubmitFromPB.
func TenantSubmitFromPB(req *pb.TenantBarrierQuorumSubmitRequest, peerThumbprint string) *seal.HolderSubmission {
	return &seal.HolderSubmission{
		Domain: seal.DomainUnseal,
		Op:     seal.QuorumOp(req.GetOp()),
		Holder: req.GetUsername(),
		Proof: seal.HolderProof{
			ChallengeID:        req.GetChallengeId(),
			Factor:             req.GetFactor(),
			Response:           req.GetResponse(),
			ResponseMetadata:   req.GetResponseMetadata(),
			PeerCertThumbprint: peerThumbprint,
		},
		Share: req.GetShare(),
	}
}

// TenantRegisterFromPB converts a tenant holder registration.
func TenantRegisterFromPB(req *pb.TenantBarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterRequest, error) {
	factors, err := FactorsFromPB(req.GetFactors())
	if err != nil {
		return nil, err
	}
	return &service.BarrierOfficerRegisterRequest{
		Username:      req.GetUsername(),
		Role:          seal.HolderRole(req.GetRole()),
		CSRPEM:        req.GetCsrPem(),
		EncryptionKEM: req.GetEncryptionKem(),
		EncryptionKey: req.GetEncryptionKey(),
		Factors:       factors,
		Designated:    req.GetDesignated(),
	}, nil
}

// TenantRegisterToPB converts a tenant registration result.
func TenantRegisterToPB(r *ceremony.RegisterOfficerResult) *pb.TenantBarrierOfficerRegisterResponse {
	return &pb.TenantBarrierOfficerRegisterResponse{
		CertPem:       r.CertPEM,
		Role:          string(r.Role),
		Designated:    r.Designated,
		EncryptionKem: r.EncryptionKEM,
		Factors:       r.Factors,
	}
}

// CeremonyInitFromPB converts a ceremony initialization request; see
// holderwire.CeremonyInitFrom. Officer Factors carry the same
// FactorsFromPB/FactorParamsError decoding RegisterFromPB uses
// (go-qrdb#4578 review item 1): CeremonyOfficer is now the same shape as
// BarrierOfficerRegisterRequest.
func CeremonyInitFromPB(req *pb.CeremonyInitializeRequest) (*service.CeremonyInitializeOptions, error) {
	var holders []service.CeremonyOfficerConfig
	if len(req.GetOfficers()) > 0 {
		holders = make([]service.CeremonyOfficerConfig, len(req.GetOfficers()))
		for i, o := range req.GetOfficers() {
			factors, err := FactorsFromPB(o.GetFactors())
			if err != nil {
				return nil, err
			}
			holders[i] = service.CeremonyOfficerConfig{
				Username:      o.GetUsername(),
				Role:          seal.HolderRole(o.GetRole()),
				CSRPEM:        o.GetCsrPem(),
				EncryptionKEM: o.GetEncryptionKem(),
				EncryptionKey: o.GetEncryptionKey(),
				Factors:       factors,
				Designated:    o.GetDesignated(),
			}
		}
	}
	// req.Secret, when non-empty, is consumed into a *secure.Buffer (secure
	// analyzer rule 3: service.CeremonyInitializeOptions.Secret is protected
	// memory, not a wire []byte) -- secure.NewBufferFromBytes wipes req's
	// Secret backing array as a side effect, so a caller no longer needs its
	// own defer secure.Wipe(req.Secret). The returned Buffer is BORROWED by
	// CeremonyInitialize exactly as the field it populates documents; the
	// caller of CeremonyInitFromPB owns it and must Destroy it once the
	// ceremony call returns.
	var secretBuf *secure.Buffer
	if len(req.GetSecret()) > 0 {
		var err error
		secretBuf, err = secure.NewBufferFromBytes(req.GetSecret())
		if err != nil {
			return nil, err
		}
	}
	return &service.CeremonyInitializeOptions{
		SOPin:                  req.GetSoPin(),
		UserPin:                req.GetUserPin(),
		CredentialSealStrategy: req.GetCredentialSealStrategy(),
		Threshold:              int(req.GetThreshold()),
		Policy:                 seal.FactorPolicy(req.GetPolicy()),
		Officers:               holders,
		Hostname:               req.GetHostname(),
		SANs:                   req.GetSans(),
		AdminCSRPEM:            req.GetAdminCsrPem(),
		AdminCertPEM:           req.GetAdminCertPem(),
		AdminCertPoolPEM:       req.GetAdminCertPoolPem(),
		AdminGenerate:          req.GetAdminGenerate(),
		Secret:                 secretBuf,
	}, nil
}

// TenantBarrierRosterFromPB converts the inline Shamir officer roster a
// `tenant barrier-init` gRPC/Unix request carries into the
// []ceremony.OfficerConfig shape ScopedCeremonyService.InitializeShamirWithRoster
// validates and registers atomically with initialization -- the protobuf
// counterpart of holderwire.TenantBarrierRosterFrom, which every other
// transport (REST, QUIC, MCP, Embedded) uses for the same roster. Returns
// nil when officers is empty.
func TenantBarrierRosterFromPB(officers []*pb.CeremonyOfficer) ([]ceremony.OfficerConfig, error) {
	if len(officers) == 0 {
		return nil, nil
	}
	out := make([]ceremony.OfficerConfig, len(officers))
	for i, o := range officers {
		factors, err := FactorsFromPB(o.GetFactors())
		if err != nil {
			return nil, err
		}
		out[i] = ceremony.OfficerConfig{
			Username:      o.GetUsername(),
			Role:          seal.HolderRole(o.GetRole()),
			CSRPEM:        []byte(o.GetCsrPem()),
			EncryptionKEM: o.GetEncryptionKem(),
			EncryptionKey: o.GetEncryptionKey(),
			Factors:       factors,
			Designated:    o.GetDesignated(),
		}
	}
	return out, nil
}

// TenantBarrierHoldersToPB converts the certificates an inline Shamir
// roster's initialization issued (tenant.TenantBarrierInitResult.Holders)
// into the wire shape TenantBarrierInitResponse carries -- the protobuf
// counterpart of holderwire.TenantBarrierHoldersTo.
func TenantBarrierHoldersToPB(holders []tenant.TenantBarrierHolderCert) []*pb.CeremonyHolderCert {
	if len(holders) == 0 {
		return nil
	}
	out := make([]*pb.CeremonyHolderCert, len(holders))
	for i, h := range holders {
		out[i] = &pb.CeremonyHolderCert{Username: h.Username, Role: h.Role, CertPem: []byte(h.CertPEM)}
	}
	return out
}

// CeremonyInitToPB converts a ceremony initialization result.
//
// AdminKeyPem is an explicit bytes.Clone of r.AdminKeyPEM, not a shared
// slice: the admin_key_pem proto field is bytes (go-qrdb#560 review item
// 7), and every transport that calls this wipes r.AdminKeyPEM
// (CeremonyInitResult.WipeAdminKeyPEM) once it has built its outgoing
// response -- sharing the backing array would zero the response's own
// copy, still unmarshaled and unsent, out from under it. The clone is the
// one, unavoidable copy this value's single delivery to its caller
// requires; nothing beyond this call and the one wipe retains another.
func CeremonyInitToPB(r *service.CeremonyInitResult) *pb.CeremonyInitializeResponse {
	holders := make([]*pb.CeremonyHolderCert, len(r.Holders))
	for i, h := range r.Holders {
		holders[i] = &pb.CeremonyHolderCert{Username: h.Username, Role: string(h.Role), CertPem: h.CertPEM}
	}
	return &pb.CeremonyInitializeResponse{
		SpkiPin:               r.SPKIPin,
		CaCertPem:             string(r.CACertPEM),
		TlsCertPem:            string(r.TLSCertPEM),
		State:                 r.State,
		ShareCount:            int32(r.ShareCount), //nolint:gosec // bounded by the holder count
		Holders:               holders,
		AdminCertPem:          string(r.AdminCertPEM),
		AdminKeyPem:           bytes.Clone(r.AdminKeyPEM),
		AdminCredentialSource: r.AdminCredentialSource,
	}
}
