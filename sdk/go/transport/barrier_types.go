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

// BarrierInitializeRequest holds the secret used to initialize the
// cryptographic barrier with a single master key.
type BarrierInitializeRequest struct {
	Secret string `json:"secret"`
}

// BarrierUnsealRequest holds the secret used to unseal the barrier.
type BarrierUnsealRequest struct {
	Secret string `json:"secret"`
}

// BarrierStatusResponse describes the current state of the cryptographic
// barrier.
type BarrierStatusResponse struct {
	Sealed         bool   `json:"sealed"`
	Strategy       string `json:"strategy"`
	HardwareBacked bool   `json:"hardware_backed"`
	InitializedAt  string `json:"initialized_at,omitempty"`
}

// BarrierInitializeShamirRequest holds the parameters for initializing the
// barrier with Shamir secret sharing.
type BarrierInitializeShamirRequest struct {
	Secret      string `json:"secret"`
	Threshold   int    `json:"threshold"`
	TotalShares int    `json:"total_shares"`
}

// BarrierInitializeShamirResponse returns the generated key shares after
// Shamir initialization.
type BarrierInitializeShamirResponse struct {
	Shares      []string `json:"shares"`
	Threshold   int      `json:"threshold"`
	TotalShares int      `json:"total_shares"`
}

// BarrierUnsealShareRequest holds a single Shamir share for incremental
// unsealing.
type BarrierUnsealShareRequest struct {
	Share string `json:"share"`
}

// BarrierUnsealShareResponse reports progress toward the unseal threshold.
type BarrierUnsealShareResponse struct {
	Required  int  `json:"required"`
	Submitted int  `json:"submitted"`
	Complete  bool `json:"complete"`
}

// BarrierUnsealSharesRequest holds multiple Shamir shares for batch
// unsealing.
type BarrierUnsealSharesRequest struct {
	Shares []string `json:"shares"`
}

// BarrierShamirSharesResponse describes the stored Shamir share metadata.
type BarrierShamirSharesResponse struct {
	Count     int `json:"count"`
	Threshold int `json:"threshold"`
	Total     int `json:"total"`
}

// BarrierShamirDeleteShareRequest identifies a single share to delete by
// its zero-based index.
type BarrierShamirDeleteShareRequest struct {
	Index int `json:"index"`
}

// BarrierRekeyRequest holds parameters for rekeying the Shamir shares.
type BarrierRekeyRequest struct {
	Threshold int `json:"threshold"`
	Total     int `json:"total"`
}

// BarrierRekeyResponse returns the new key shares after a rekey operation.
type BarrierRekeyResponse struct {
	Shares      []string `json:"shares"`
	Threshold   int      `json:"threshold"`
	TotalShares int      `json:"total_shares"`
}

// BarrierGenerateRecoveryKeysRequest holds parameters for generating
// recovery keys.
type BarrierGenerateRecoveryKeysRequest struct {
	Threshold int `json:"threshold"`
	Total     int `json:"total"`
}

// BarrierRecoveryKeysResponse returns generated recovery keys.
type BarrierRecoveryKeysResponse struct {
	Keys      []string `json:"keys"`
	Threshold int      `json:"threshold"`
	Total     int      `json:"total"`
}

// BarrierRecoverWithKeysRequest holds recovery keys used to unseal the
// barrier during disaster recovery.
type BarrierRecoverWithKeysRequest struct {
	Keys []string `json:"keys"`
}

// BarrierHasRecoveryKeysResponse indicates whether recovery keys have been
// generated for the barrier.
type BarrierHasRecoveryKeysResponse struct {
	HasKeys bool `json:"has_keys"`
}

// BarrierGenerateRootTokenRequest holds the Shamir shares required to
// generate a new root token.
type BarrierGenerateRootTokenRequest struct {
	Shares []string `json:"shares"`
}

// BarrierRootTokenResponse returns a newly generated root token.
type BarrierRootTokenResponse struct {
	Token     string `json:"token"`
	CreatedAt string `json:"created_at"`
}
