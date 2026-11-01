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

package service

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
)

// ProvisionInitializePINs is the PIN step of both barrier initialize paths
// (credential and Shamir), shared by every transport. It refuses an
// initialize the barrier service would refuse -- with
// *SystemBarrierInitRefusedError on a service built with a ceremony, with
// seal.ErrAlreadyInitialized on an initialized barrier -- BEFORE any PIN is
// written or compared, and only then provisions -- or, when they are already
// set, verifies -- the supplied PINs through pin.Provision.
//
// The order is the point. A refused initialize writes nothing: provisioned
// first, the PINs of a call the service then refuses would outlive the
// refusal and be the PINs the real bootstrap later finds already set.
// Initialization is also reachable anonymously on the sealed-window listener,
// and pin.Provision verifies a supplied SO PIN that is already set; checked
// first, it would answer a right PIN and a wrong one differently on a barrier
// that can never be initialized this way, making every call a free online
// guess at the SO PIN. Refused first, the call compares nothing and spends
// nothing against the SO PIN lockout.
//
// A failure to read the barrier's initialization state is returned: the
// refusal fails closed rather than falling through to the PIN step.
func ProvisionInitializePINs(ctx context.Context, barrier BarrierServicer, pins pin.PINBackend, soPIN, userPIN string) error {
	if err := barrier.BarrierInitializePrecondition(ctx); err != nil {
		return err
	}
	return pin.Provision(ctx, pins, soPIN, userPIN)
}
