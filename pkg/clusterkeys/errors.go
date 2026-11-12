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

package clusterkeys

import "errors"

// Sentinel errors for the cluster-peer key transport layer.
// Callers match these with errors.Is.
var (
	// ErrNilReceiver indicates a nil keydelivery.Receiver was provided.
	ErrNilReceiver = errors.New("clusterkeys: keydelivery receiver must not be nil")

	// ErrNilPlacements indicates a nil PlacementGetter was provided.
	ErrNilPlacements = errors.New("clusterkeys: placement getter must not be nil")

	// ErrEmptySelfNodeID indicates an empty self-node ID was provided.
	ErrEmptySelfNodeID = errors.New("clusterkeys: self node ID must not be empty")

	// ErrEmptyDeliveryCert indicates no delivery certificate PEM was provided.
	ErrEmptyDeliveryCert = errors.New("clusterkeys: delivery certificate PEM must not be empty")

	// ErrPlacementGate indicates the placement table has no entry assigning
	// a replica of the requested tenant/database to this node.
	ErrPlacementGate = errors.New("clusterkeys: no placement entry for this node")

	// ErrMalformedBody indicates the POST body could not be decoded as a valid
	// keydelivery.Envelope.
	ErrMalformedBody = errors.New("clusterkeys: malformed request body")

	// ErrUnknownMode indicates the delivery envelope carries a mode value
	// not handled by the mode dispatch table.
	ErrUnknownMode = errors.New("clusterkeys: unknown delivery mode")
)
