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

// LockInfo describes an acquired distributed lock.
type LockInfo struct {
	Key          string `json:"key"`
	FencingToken int64  `json:"fencing_token"`
	LeaseID      int64  `json:"lease_id"`
	HolderID     string `json:"holder_id"`
	Reentrant    bool   `json:"reentrant"`
	AcquireCount int32  `json:"acquire_count"`
	TTL          int64  `json:"ttl"`
	AcquiredAt   int64  `json:"acquired_at"`
}

// LockAcquireRequest contains parameters for acquiring a distributed lock.
type LockAcquireRequest struct {
	Key       string `json:"key"`
	HolderID  string `json:"holder_id,omitempty"`
	TTL       int64  `json:"ttl"`
	Reentrant bool   `json:"reentrant,omitempty"`
}

// LockReleaseRequest contains parameters for releasing a distributed lock.
type LockReleaseRequest struct {
	Key          string `json:"key"`
	FencingToken int64  `json:"fencing_token"`
}

// LockExtendRequest contains parameters for extending a lock's TTL.
type LockExtendRequest struct {
	Key          string `json:"key"`
	FencingToken int64  `json:"fencing_token"`
}

// LockListResponse contains a list of active distributed locks.
type LockListResponse struct {
	Locks []*LockInfo `json:"locks"`
}
