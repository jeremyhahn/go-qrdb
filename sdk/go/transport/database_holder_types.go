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

// Shamir custody of a database barrier. A database is one more holder scope
// of the share-holder model: each request names the database and otherwise
// carries the system holder request it embeds, whose fields mean what they
// mean there. A database barrier has the unseal domain only, so a Domain the
// embedded request carries is ignored.

// DatabaseOfficerRegisterRequest enrols a share holder of a database's
// barrier.
type DatabaseOfficerRegisterRequest struct {
	Database string `json:"database"`
	OfficerRegisterRequest
}

// DatabaseShamirInitializeRequest initializes a database barrier's Shamir
// custody: the root key is split to the enrolled holders at the M-of-N the
// database was created with, under Policy.
type DatabaseShamirInitializeRequest struct {
	Database string `json:"database"`
	Policy   string `json:"policy,omitempty"`
}

// DatabaseShareClaimBeginRequest opens a database holder's claim handshake.
type DatabaseShareClaimBeginRequest struct {
	Database string `json:"database"`
	ShareClaimBeginRequest
}

// DatabaseShareClaimCompleteRequest completes a database holder's claim.
type DatabaseShareClaimCompleteRequest struct {
	Database string `json:"database"`
	ShareClaimCompleteRequest
}

// DatabaseQuorumBeginRequest asks for a database holder's quorum challenge.
type DatabaseQuorumBeginRequest struct {
	Database string `json:"database"`
	QuorumBeginRequest
}

// DatabaseQuorumSubmitRequest submits one database holder's share. The
// caller owns Share and must wipe it after the call.
type DatabaseQuorumSubmitRequest struct {
	Database string `json:"database"`
	QuorumSubmitRequest
}

// DatabaseRekeyStartRequest opens a rekey round on a database barrier.
type DatabaseRekeyStartRequest struct {
	Database  string `json:"database"`
	Threshold int    `json:"threshold"`
	Policy    string `json:"policy,omitempty"`
}

// DatabaseNameRequiredError is returned by a client when a database
// share-holder request is nil or names no database: there is no scope to
// address.
type DatabaseNameRequiredError struct {
	// Method is the client method that refused.
	Method string
}

// Error returns a human-readable description.
func (e *DatabaseNameRequiredError) Error() string {
	return "transport: " + e.Method + " requires a database name"
}

// DatabaseOfficerUnenrolRequest removes a database holder before the
// database's custody is initialized, when it holds no share.
type DatabaseOfficerUnenrolRequest struct {
	Database string `json:"database"`
	Username string `json:"username"`
}

// DatabaseOfficerRevokeRequest revokes a database holder: a rekey round that
// removes them, completed by M of the remaining holders.
type DatabaseOfficerRevokeRequest struct {
	Database  string `json:"database"`
	Username  string `json:"username"`
	Threshold int    `json:"threshold,omitempty"`
}
