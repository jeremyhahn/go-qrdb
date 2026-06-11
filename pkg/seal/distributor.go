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

import "context"

// CeremonyDistributor seals Shamir share material to enrolled Security
// Officers via the ceremony subsystem. pkg/seal defines the interface
// (consumer side); pkg/ceremony provides the implementations. The secret
// passed to either method is wiped before the call returns, regardless of
// outcome.
type CeremonyDistributor interface {
	// SealAndDistribute splits shareSecret into total shares with the given
	// threshold and seals one share to each enrolled officer's enclave.
	// Returns the number of shares sealed.
	SealAndDistribute(ctx context.Context, shareSecret []byte, threshold, total int) (int, error)

	// RekeyAndDistribute destroys all unclaimed share enclaves from the
	// prior round, resets claim state, and seals new shares split from
	// newShareSecret. Returns the number of shares sealed.
	RekeyAndDistribute(ctx context.Context, newShareSecret []byte, newThreshold, newTotal int) (int, error)
}
