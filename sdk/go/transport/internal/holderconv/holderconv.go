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

// Package holderconv converts the share-holder messages between the
// transport-neutral types and their protobuf form. The gRPC, Unix and QUIC
// transports exchange the SAME protobuf messages, so the conversion lives in
// exactly one place. It is internal: only the transport implementations may
// import it.
package holderconv

import (
	"encoding/json"
	"time"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// FactorParamsError is returned when a factor enrolment's params cannot be
// encoded.
type FactorParamsError struct {
	Factor string
	Err    error
}

// Error returns a human-readable description.
func (e *FactorParamsError) Error() string {
	return "holderconv: factor " + e.Factor + " params: " + e.Err.Error()
}

// Unwrap returns the encode error.
func (e *FactorParamsError) Unwrap() error { return e.Err }

// parseTime parses an RFC 3339 timestamp; an empty or malformed one yields
// the zero time.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// factorsToPB encodes factor enrolments.
func factorsToPB(in []transport.HolderFactorEnrolment) ([]*pb.HolderFactorEnrolment, error) {
	out := make([]*pb.HolderFactorEnrolment, 0, len(in))
	for _, f := range in {
		var params []byte
		if len(f.Params) > 0 {
			raw, err := json.Marshal(f.Params)
			if err != nil {
				return nil, &FactorParamsError{Factor: f.Name, Err: err}
			}
			params = raw
		}
		out = append(out, &pb.HolderFactorEnrolment{Name: f.Name, ParamsJson: params})
	}
	return out, nil
}

// RegisterToPB encodes a system-barrier holder registration.
func RegisterToPB(req *transport.OfficerRegisterRequest) (*pb.BarrierOfficerRegisterRequest, error) {
	factors, err := factorsToPB(req.Factors)
	if err != nil {
		return nil, err
	}
	return &pb.BarrierOfficerRegisterRequest{
		Username:      req.Username,
		Role:          req.Role,
		CsrPem:        []byte(req.CSRPEM),
		EncryptionKem: req.EncryptionKEM,
		EncryptionKey: req.EncryptionKey,
		Factors:       factors,
		Designated:    req.Designated,
	}, nil
}

// TenantRegisterToPB encodes a tenant-barrier holder registration.
func TenantRegisterToPB(req *transport.TenantOfficerRegisterRequest) (*pb.TenantBarrierOfficerRegisterRequest, error) {
	factors, err := factorsToPB(req.Factors)
	if err != nil {
		return nil, err
	}
	return &pb.TenantBarrierOfficerRegisterRequest{
		TenantId:      req.TenantID,
		Username:      req.Username,
		Role:          req.Role,
		CsrPem:        []byte(req.CSRPEM),
		EncryptionKem: req.EncryptionKEM,
		EncryptionKey: req.EncryptionKey,
		Factors:       factors,
		Designated:    req.Designated,
	}, nil
}

// RegisterFromPB decodes a system-barrier registration result.
func RegisterFromPB(resp *pb.BarrierOfficerRegisterResponse) *transport.OfficerRegisterResponse {
	return &transport.OfficerRegisterResponse{
		CertPEM:       string(resp.GetCertPem()),
		Role:          resp.GetRole(),
		Designated:    resp.GetDesignated(),
		EncryptionKEM: resp.GetEncryptionKem(),
		Factors:       resp.GetFactors(),
	}
}

// TenantRegisterFromPB decodes a tenant-barrier registration result.
func TenantRegisterFromPB(resp *pb.TenantBarrierOfficerRegisterResponse) *transport.OfficerRegisterResponse {
	return &transport.OfficerRegisterResponse{
		CertPEM:       string(resp.GetCertPem()),
		Role:          resp.GetRole(),
		Designated:    resp.GetDesignated(),
		EncryptionKEM: resp.GetEncryptionKem(),
		Factors:       resp.GetFactors(),
	}
}

// StatusesFromPB decodes holder status snapshots.
func StatusesFromPB(officers []*pb.OfficerStatus) []*transport.OfficerStatus {
	out := make([]*transport.OfficerStatus, 0, len(officers))
	for _, o := range officers {
		out = append(out, &transport.OfficerStatus{
			Username:       o.GetUsername(),
			Role:           o.GetRole(),
			CertThumbprint: o.GetCertThumbprint(),
			ShareIndex:     int(o.GetShareIndex()),
			Designated:     o.GetDesignated(),
			Factors:        o.GetFactors(),
			EncryptionKEM:  o.GetEncryptionKem(),
			EnrolledAt:     parseTime(o.GetEnrolledAt()),
			ClaimedAt:      parseTime(o.GetClaimedAt()),
			SharePending:   o.GetSharePending(),
		})
	}
	return out
}

// ClaimBeginFromPB decodes a claim-begin response.
func ClaimBeginFromPB(nonceHex, expiresAt string) *transport.ShareClaimBeginResponse {
	return &transport.ShareClaimBeginResponse{NonceHex: nonceHex, ExpiresAt: parseTime(expiresAt)}
}

// BeginToPB encodes a quorum challenge request.
func BeginToPB(req *transport.QuorumBeginRequest) *pb.BarrierQuorumBeginRequest {
	return &pb.BarrierQuorumBeginRequest{Domain: req.Domain, Op: req.Op, Username: req.Username, Factor: req.Factor}
}

// TenantBeginToPB encodes a tenant quorum challenge request.
func TenantBeginToPB(req *transport.TenantQuorumBeginRequest) *pb.TenantBarrierQuorumBeginRequest {
	return &pb.TenantBarrierQuorumBeginRequest{TenantId: req.TenantID, Op: req.Op, Username: req.Username, Factor: req.Factor}
}

// ChallengeFromPB decodes a quorum challenge.
func ChallengeFromPB(c *pb.BarrierQuorumChallenge) *transport.QuorumChallenge {
	return &transport.QuorumChallenge{
		ChallengeID: c.GetChallengeId(),
		Domain:      c.GetDomain(),
		Op:          c.GetOp(),
		Username:    c.GetUsername(),
		Factor:      c.GetFactor(),
		Nonce:       c.GetNonce(),
		MethodData:  c.GetMethodData(),
		ExpiresAt:   parseTime(c.GetExpiresAt()),
	}
}

// SubmitToPB encodes a quorum submission.
func SubmitToPB(req *transport.QuorumSubmitRequest) *pb.BarrierQuorumSubmitRequest {
	return &pb.BarrierQuorumSubmitRequest{
		Domain:           req.Domain,
		Op:               req.Op,
		Username:         req.Username,
		ChallengeId:      req.ChallengeID,
		Factor:           req.Factor,
		Response:         req.Response,
		ResponseMetadata: req.ResponseMetadata,
		Share:            req.Share,
	}
}

// TenantSubmitToPB encodes a tenant quorum submission.
func TenantSubmitToPB(req *transport.TenantQuorumSubmitRequest) *pb.TenantBarrierQuorumSubmitRequest {
	return &pb.TenantBarrierQuorumSubmitRequest{
		TenantId:         req.TenantID,
		Op:               req.Op,
		Username:         req.Username,
		ChallengeId:      req.ChallengeID,
		Factor:           req.Factor,
		Response:         req.Response,
		ResponseMetadata: req.ResponseMetadata,
		Share:            req.Share,
	}
}

// OutcomeFromPB decodes a submission outcome.
func OutcomeFromPB(o *pb.BarrierQuorumOutcome) *transport.QuorumOutcome {
	return &transport.QuorumOutcome{
		Domain:             o.GetDomain(),
		Op:                 o.GetOp(),
		Required:           int(o.GetRequired()),
		Submitted:          int(o.GetSubmitted()),
		Complete:           o.GetComplete(),
		RootToken:          o.GetRootToken(),
		RootTokenCreatedAt: parseTime(o.GetRootTokenCreatedAt()),
		Threshold:          int(o.GetThreshold()),
		TotalShares:        int(o.GetTotalShares()),
		ShareCount:         int(o.GetShareCount()),
	}
}

// RoundFromPB decodes an open round.
func RoundFromPB(r *pb.BarrierQuorumRound) *transport.QuorumRound {
	return &transport.QuorumRound{
		Domain:       r.GetDomain(),
		Op:           r.GetOp(),
		Required:     int(r.GetRequired()),
		Submitted:    int(r.GetSubmitted()),
		ExpiresAt:    parseTime(r.GetExpiresAt()),
		NewThreshold: int(r.GetNewThreshold()),
		Policy:       r.GetPolicy(),
		Remove:       r.GetRemove(),
	}
}

// RekeyToPB encodes a rekey-round request.
func RekeyToPB(req *transport.RekeyStartRequest) *pb.BarrierRekeyStartRequest {
	return &pb.BarrierRekeyStartRequest{Domain: req.Domain, Threshold: int32(req.Threshold), Policy: req.Policy, SoPin: req.SOPin} //nolint:gosec
}

// RevokeToPB encodes a holder revocation.
func RevokeToPB(req *transport.OfficerRevokeRequest) *pb.BarrierOfficerRevokeRequest {
	return &pb.BarrierOfficerRevokeRequest{Domain: req.Domain, Username: req.Username, Threshold: int32(req.Threshold), SoPin: req.SOPin} //nolint:gosec
}

// TenantRekeyToPB encodes a tenant rekey-round request.
func TenantRekeyToPB(req *transport.TenantRekeyStartRequest) *pb.TenantBarrierRekeyStartRequest {
	return &pb.TenantBarrierRekeyStartRequest{TenantId: req.TenantID, Threshold: int32(req.Threshold), Policy: req.Policy} //nolint:gosec
}

// TenantRevokeToPB encodes a tenant holder revocation.
func TenantRevokeToPB(req *transport.TenantOfficerRevokeRequest) *pb.TenantBarrierOfficerRevokeRequest {
	return &pb.TenantBarrierOfficerRevokeRequest{TenantId: req.TenantID, Username: req.Username, Threshold: int32(req.Threshold)} //nolint:gosec
}

// CeremonyInitToPB encodes a ceremony initialization. Officer Factors carry
// the same factorsToPB/FactorParamsError encoding RegisterToPB uses
// (go-qrdb#4578 review item 1): CeremonyOfficer is now the same shape as
// OfficerRegisterRequest.
//
// Secret is now bytes end to end (go-qrdb#560 review item 7, follow-up):
// transport.CeremonyInitializeRequest.Secret is []byte, the same as the
// proto field, so this is a direct assignment, not a copy -- safe because
// the gRPC client call this builds the request for is synchronous: it
// marshals and sends req before this function's caller can regain control
// to wipe anything.
func CeremonyInitToPB(req *transport.CeremonyInitializeRequest) (*pb.CeremonyInitializeRequest, error) {
	officers := make([]*pb.CeremonyOfficer, len(req.Officers))
	for i, o := range req.Officers {
		factors, err := factorsToPB(o.Factors)
		if err != nil {
			return nil, err
		}
		officers[i] = &pb.CeremonyOfficer{
			Username:      o.Username,
			Role:          o.Role,
			CsrPem:        o.CSRPEM,
			EncryptionKem: o.EncryptionKEM,
			EncryptionKey: o.EncryptionKey,
			Factors:       factors,
			Designated:    o.Designated,
		}
	}
	return &pb.CeremonyInitializeRequest{
		SoPin:                  req.SOPin,
		UserPin:                req.UserPin,
		CredentialSealStrategy: req.CredentialSealStrategy,
		Threshold:              int32(req.Threshold), //nolint:gosec
		Policy:                 req.Policy,
		Officers:               officers,
		Hostname:               req.Hostname,
		Sans:                   req.SANs,
		AdminCsrPem:            req.AdminCSRPEM,
		AdminCertPem:           req.AdminCertPEM,
		AdminCertPoolPem:       req.AdminCertPoolPEM,
		AdminGenerate:          req.AdminGenerate,
		Secret:                 req.Secret,
	}, nil
}

// CeremonyInitFromPB decodes a ceremony initialization result.
//
// AdminKeyPEM is now bytes end to end (go-qrdb#560 review item 7,
// follow-up): transport.CeremonyInitializeResponse.AdminKeyPEM is []byte,
// the same as the proto field, so this is a direct assignment, not a
// copy -- resp was freshly unmarshaled by grpc-go for this call alone, so
// nothing else retains or mutates its backing array once this function
// hands it to the caller, who is responsible for wiping it.
func CeremonyInitFromPB(resp *pb.CeremonyInitializeResponse) *transport.CeremonyInitializeResponse {
	holders := make([]transport.CeremonyHolderCert, len(resp.GetHolders()))
	for i, h := range resp.GetHolders() {
		holders[i] = transport.CeremonyHolderCert{Username: h.GetUsername(), Role: h.GetRole(), CertPEM: string(h.GetCertPem())}
	}
	return &transport.CeremonyInitializeResponse{
		SPKIPin:               resp.GetSpkiPin(),
		CACertPEM:             resp.GetCaCertPem(),
		TLSCertPEM:            resp.GetTlsCertPem(),
		State:                 resp.GetState(),
		ShareCount:            int(resp.GetShareCount()),
		Holders:               holders,
		AdminCertPEM:          resp.GetAdminCertPem(),
		AdminKeyPEM:           resp.GetAdminKeyPem(),
		AdminCredentialSource: resp.GetAdminCredentialSource(),
	}
}

// TenantBarrierRosterToPB encodes the inline Shamir officer roster a
// `tenant barrier-init` request carries into the []*pb.CeremonyOfficer
// shape the gRPC/Unix wire uses -- the same conversion CeremonyInitToPB
// applies for the system barrier's own inline roster, since CeremonyOfficer
// is the same shape as OfficerRegisterRequest.
func TenantBarrierRosterToPB(officers []transport.OfficerRegisterRequest) ([]*pb.CeremonyOfficer, error) {
	if len(officers) == 0 {
		return nil, nil
	}
	out := make([]*pb.CeremonyOfficer, len(officers))
	for i, o := range officers {
		factors, err := factorsToPB(o.Factors)
		if err != nil {
			return nil, err
		}
		out[i] = &pb.CeremonyOfficer{
			Username:      o.Username,
			Role:          o.Role,
			CsrPem:        o.CSRPEM,
			EncryptionKem: o.EncryptionKEM,
			EncryptionKey: o.EncryptionKey,
			Factors:       factors,
			Designated:    o.Designated,
		}
	}
	return out, nil
}

// TenantBarrierHoldersFromPB decodes the certificates an inline Shamir
// roster's initialization issued, in roster order, from the wire shape
// TenantBarrierInitResponse carries.
func TenantBarrierHoldersFromPB(holders []*pb.CeremonyHolderCert) []transport.TenantBarrierHolderCert {
	if len(holders) == 0 {
		return nil
	}
	out := make([]transport.TenantBarrierHolderCert, len(holders))
	for i, h := range holders {
		out[i] = transport.TenantBarrierHolderCert{Username: h.GetUsername(), Role: h.GetRole(), CertPEM: string(h.GetCertPem())}
	}
	return out
}
