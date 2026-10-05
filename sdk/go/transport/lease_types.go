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

// LeaseInfo describes a distributed TTL lease.
type LeaseInfo struct {
	ID           int64    `json:"id"`
	TTL          int64    `json:"ttl"`
	GrantedTTL   int64    `json:"granted_ttl"`
	RemainingTTL int64    `json:"remaining_ttl"`
	Keys         [][]byte `json:"keys,omitempty"`
}

// LeaseGrantResponse is returned by LeaseGrant.
type LeaseGrantResponse struct {
	Lease *LeaseInfo `json:"lease"`
}

// LeaseKeepAliveResponse is returned by LeaseKeepAlive.
type LeaseKeepAliveResponse struct {
	Lease *LeaseInfo `json:"lease"`
}

// LeaseTimeToLiveResponse is returned by LeaseTimeToLive.
type LeaseTimeToLiveResponse struct {
	Lease *LeaseInfo `json:"lease"`
}

// LeaseListResponse is returned by LeaseList.
type LeaseListResponse struct {
	Leases []*LeaseInfo `json:"leases"`
}

// FirstLeaseID returns the first non-zero LeaseID named by ops, in the
// order given (success ops before failure ops, each remote transport's own
// convention when scanning an ExecuteTransaction request), and whether one
// was found.
//
// Shared by every remote transport's ExecuteTransaction/ExecuteBatch
// NotFound mapping (go-qrdb#591's review, non-blocking: this loop was
// duplicated five times, once per transport, before being extracted
// here): checkResultError's only NotFound-classed outcome for a
// transaction or batch command is a write naming a lease the state
// machine does not hold, so once a transport has already established
// that a response IS specifically a NotFound (its own wire-level signal
// -- a gRPC/Unix status code, a QUIC/MCP reconstructed not-found, or,
// for REST, both the 404 status AND the server's message confirming it,
// since REST carries no structured error code), the lease ID named in
// the SAME request this response answers is read back out of the
// request's own ops rather than guessed from the server's message text
// (the substring classification go-qrdb#289 deleted).
func FirstLeaseID(ops []Op) (int64, bool) {
	for _, op := range ops {
		if op.LeaseID != 0 {
			return op.LeaseID, true
		}
	}
	return 0, false
}
