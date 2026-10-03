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

package clusterseal

import "time"

// Report is the `cluster_seal` object every barrier status carries on every
// transport, and the answer to a cluster unseal (docs/barrier-seal/api.md).
type Report struct {
	// Active is true once the cluster has activated cluster-seal. When it is
	// false every seal is node-local and the other fields are zero.
	Active bool `json:"active"`
	// Sealed and Epoch are the barrier's record; Epoch 0 means none exists.
	Sealed bool   `json:"sealed"`
	Epoch  uint64 `json:"epoch"`
	// Principal, PrincipalTenant, Node and At say who wrote the record, from
	// which tenant, through which node, and when.
	Principal       string    `json:"principal,omitempty"`
	PrincipalTenant string    `json:"principal_tenant,omitempty"`
	Node            string    `json:"node,omitempty"`
	At              time.Time `json:"at,omitzero"`
	// Local is this node's own state for the barrier.
	Local Local `json:"local,omitempty"`
}

// Report converts the status to its wire shape.
func (s Status) Report() *Report {
	if !s.Active {
		return &Report{}
	}
	return &Report{
		Active:          true,
		Sealed:          s.Record.Sealed,
		Epoch:           s.Record.Epoch,
		Principal:       s.Record.Principal,
		PrincipalTenant: s.Record.PrincipalTenant,
		Node:            s.Record.Node,
		At:              s.Record.At,
		Local:           s.Local,
	}
}

// ReportOf reports b through sealer, which may be nil: a composition with no
// sealer is a node whose seals are node-local.
func ReportOf(sealer Sealer, b Barrier) *Report {
	if sealer == nil {
		return &Report{}
	}
	return sealer.Status(b).Report()
}

// ActiveIn reports whether sealer, which may be nil, seals cluster-wide.
func ActiveIn(sealer Sealer) bool {
	return sealer != nil && sealer.Active()
}
