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

// ShamirInitParams carries the threshold, total-share count, and officer
// share count for a Shamir initialization or rekey. Unlike ShamirInitResult
// it carries no share material; it is the share-free return type used after
// share distribution has been delegated to the CeremonyDistributor.
type ShamirInitParams struct {
	// Threshold is the minimum number of shares required to reconstruct the
	// secret (M in M-of-N).
	Threshold int `json:"threshold"`

	// TotalShares is the total number of shares produced (N in M-of-N).
	TotalShares int `json:"total_shares"`

	// ShareCount is the number of officer shares that were sealed into
	// ceremony enclaves. This equals TotalShares when all officers are
	// enrolled; it may be less if enrollment is still in progress.
	ShareCount int `json:"share_count"`
}
