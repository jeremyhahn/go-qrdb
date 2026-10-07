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

package audit

// QueryRequest is the sm.DiskStateMachine Lookup query that answers a
// replicated-chain Query: both PebbleStateMachine and MVCCEngine recognise
// it in their Lookup dispatch, mirroring lease.LeaseTimeToLiveQuery exactly,
// and answer with a *Page. TenantID is the confined tenant the caller is
// already restricted to (tenant.ConfineAuditFilter runs before this request
// is built), never a value read from Filter.
type QueryRequest struct {
	TenantID string
	Filter   Filter
}

// VerifyRequest is the sm.DiskStateMachine Lookup query that answers a
// replicated-chain hash-continuity check for one tenant: both
// PebbleStateMachine and MVCCEngine recognise it in their Lookup dispatch,
// alongside QueryRequest, and answer with a *ChainVerification. TenantID is
// the confined tenant the caller is already restricted to, exactly as for
// QueryRequest.
type VerifyRequest struct {
	TenantID string
}
