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
