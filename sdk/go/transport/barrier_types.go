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

// BarrierInitializeRequest holds the parameters used to initialize the
// cryptographic barrier. The canonical CLI verb tree (qrdb / xkmsctl /
// quicbaas / platformctl) exposes:
//
//	barrier init --barrier-strategy <none|software|shamir>
//	             --secret <passphrase>     (required for software and shamir)
//	             --so-pin <pin>            (optional)
//	             --user-pin <pin>          (required if --so-pin set)
//
// BarrierStrategy + SOPin + UserPin are optional on the wire to remain
// backward compatible with servers that pre-date the canonical surface.
//
// Secret is the strategy wrapping passphrase. SOPin/UserPin, when
// provided, are routed to the canonical pin.PINBackend on the server
// (qrdb/pkg/pin) — independent of and complementary to Secret.
type BarrierInitializeRequest struct {
	Secret          string `json:"secret"`
	BarrierStrategy string `json:"barrier_strategy,omitempty"`
	SOPin           string `json:"so_pin,omitempty"`
	UserPin         string `json:"user_pin,omitempty"`
}

// BarrierUnsealRequest holds the secret used to unseal the barrier when
// the barrier was initialized with a single-master-key strategy
// (--barrier-strategy=software). PIN fields are intentionally absent —
// unseal uses the strategy's passphrase directly and requires no PIN
// authorization.
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

// BarrierSealRequest carries the optional Security Officer PIN supplied
// when sealing the barrier. The SO PIN is verified against the canonical
// pin.PINBackend when one is configured and a SO PIN has been set; when
// no backend or PIN is configured the field is ignored.
//
// This type is part of the canonical SO-PIN-gated administrative-op
// surface. Older clients that call BarrierSeal without a body still work
// because the server treats a missing SOPin as a no-op when no PIN
// backend is configured.
type BarrierSealRequest struct {
	SOPin string `json:"so_pin,omitempty"`
}

// BarrierInitializeShamirRequest holds the parameters for initializing the
// barrier with Shamir secret sharing M-of-N. The canonical CLI verb is
// `barrier init-shamir`.
//
// SOPin/UserPin, when provided, are routed to the canonical
// pin.PINBackend on the server — independent of and complementary to
// Secret.
type BarrierInitializeShamirRequest struct {
	Secret      string `json:"secret"`
	Threshold   int    `json:"threshold"`
	TotalShares int    `json:"total_shares"`
	SOPin       string `json:"so_pin,omitempty"`
	UserPin     string `json:"user_pin,omitempty"`
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
// unsealing. The canonical CLI verb is `barrier unseal-shares`.
type BarrierUnsealSharesRequest struct {
	Shares []string `json:"shares"`
}

// BarrierRekeyRequest holds parameters for rekeying the barrier. The
// canonical CLI verb is `barrier rekey` and accepts the new passphrase,
// an optional SO PIN (required when a PINBackend is configured), and
// optional Shamir threshold/total when the strategy is shamir.
//
// SOPin, when provided, is verified against the canonical pin.PINBackend
// before the rekey is executed. When no backend or PIN is configured the
// field is ignored, preserving dev-mode flows.
type BarrierRekeyRequest struct {
	NewSecret string `json:"new_secret,omitempty"`
	SOPin     string `json:"so_pin,omitempty"`
	Threshold int    `json:"threshold"`
	Total     int    `json:"total"`
}

// BarrierRekeyResponse returns the new key shares after a rekey operation.
type BarrierRekeyResponse struct {
	Shares      []string `json:"shares"`
	Threshold   int      `json:"threshold"`
	TotalShares int      `json:"total_shares"`
}

// BarrierGenerateRecoveryKeysRequest holds parameters for generating
// recovery keys. The canonical CLI verb is `barrier recovery generate`
// and supports an optional SO PIN.
type BarrierGenerateRecoveryKeysRequest struct {
	Threshold int    `json:"threshold"`
	Total     int    `json:"total"`
	SOPin     string `json:"so_pin,omitempty"`
}

// BarrierRecoveryKeysResponse returns generated recovery keys.
type BarrierRecoveryKeysResponse struct {
	Keys      []string `json:"keys"`
	Threshold int      `json:"threshold"`
	Total     int      `json:"total"`
}

// BarrierRecoverWithKeysRequest holds recovery keys used to unseal the
// barrier during disaster recovery. The canonical CLI verb is
// `barrier recovery recover` with repeatable `--share` flags.
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
