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

package seal

// ShamirInitParams carries the threshold, total-share count and sealed-share
// count of a Shamir initialization or rekey. It carries no share material:
// shares reach their holders only as envelopes sealed by the
// CeremonyDistributor.
type ShamirInitParams struct {
	// Threshold is the minimum number of shares required to reconstruct the
	// secret (M in M-of-N).
	Threshold int `json:"threshold"`

	// TotalShares is the total number of shares produced (N in M-of-N).
	TotalShares int `json:"total_shares"`

	// ShareCount is the number of share envelopes the distributor sealed,
	// one per holder in the roster. A distribution never seals fewer shares
	// than TotalShares: a holder count that differs from the requested total
	// is refused with HolderCountMismatchError.
	ShareCount int `json:"share_count"`

	// UnsealKeyCheck is the check value of the unseal key the shares
	// reconstruct (UnsealKeyCheck). The owner signs it into the barrier's
	// init statement, and every other serving node checks its first quorum
	// against it (ArmShamir). Empty for the recovery domain.
	UnsealKeyCheck []byte `json:"unseal_key_check,omitempty"`

	// Roster is the encoded holder roster the shares were split to; its
	// SHA-256 is the init statement's roster hash. Empty for the recovery
	// domain.
	Roster []byte `json:"roster,omitempty"`
}
