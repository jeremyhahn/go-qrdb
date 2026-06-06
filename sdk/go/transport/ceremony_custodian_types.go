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

package transport

import "time"

// CeremonyClaimShareBeginRequest names the enrolled custodian beginning a
// Shamir-share claim.
type CeremonyClaimShareBeginRequest struct {
	CustodianID string `json:"custodianID"`
}

// CeremonyClaimShareBeginResponse carries the factor challenge the custodian
// must answer to claim their share.
type CeremonyClaimShareBeginResponse struct {
	CustodianID string    `json:"custodianID"`
	Nonce       []byte    `json:"nonce"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

// CeremonyClaimShareCompleteRequest carries the custodian's factor response
// to complete a share claim.
type CeremonyClaimShareCompleteRequest struct {
	CustodianID    string `json:"custodianID"`
	ChallengeID    string `json:"challengeID"`
	FactorResponse []byte `json:"factorResponse,omitempty"`
}

// CeremonyCustodianInfo is the custodian metadata returned after a successful
// share claim. JSON tags mirror the REST custodian payload.
type CeremonyCustodianInfo struct {
	ID             string     `json:"id"`
	Username       string     `json:"username"`
	TenantID       string     `json:"tenantID"`
	Method         string     `json:"method"`
	EnrollmentID   string     `json:"enrollmentID"`
	PublicKeyPEM   string     `json:"publicKeyPEM"`
	ShareIndex     int        `json:"shareIndex"`
	EnrolledAt     time.Time  `json:"enrolledAt"`
	RevokedAt      *time.Time `json:"revokedAt,omitempty"`
	ShareClaimed   bool       `json:"shareClaimed"`
	ShareClaimedAt *time.Time `json:"shareClaimedAt,omitempty"`
}

// CeremonySubmitUnsealShareRequest carries a custodian's plaintext Shamir
// share plus factor proof toward the unseal quorum.
type CeremonySubmitUnsealShareRequest struct {
	CustodianID    string `json:"custodianID"`
	ChallengeID    string `json:"challengeID"`
	FactorResponse []byte `json:"factorResponse,omitempty"`
	ShareBytes     []byte `json:"shareBytes"`
}

// CeremonySubmitUnsealShareResponse reports quorum progress after a share
// submission.
type CeremonySubmitUnsealShareResponse struct {
	Submitted int  `json:"submitted"`
	Required  int  `json:"required"`
	Unsealed  bool `json:"unsealed"`
}
