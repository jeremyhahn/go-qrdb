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

package replicaforward

import "github.com/jeremyhahn/go-qrdb/pkg/cluster"

// NoTargetServedError reports that no replica served a forwarded request: none
// advertises the protocol, none was reachable, or both attempts were refused.
// The forwarding layer answers the caller with the original
// *placement.NotAReplicaError, which carries the replica set.
type NoTargetServedError struct {
	TenantID string
	Database string
	Protocol cluster.APIProtocol
}

// Error implements the error interface.
func (e *NoTargetServedError) Error() string {
	return "replicaforward: no replica of " + e.TenantID + "/" + e.Database + " served the request over " + string(e.Protocol)
}

// MemberLookupError reports that the member records could not be read.
type MemberLookupError struct {
	Err error
}

// Error implements the error interface.
func (e *MemberLookupError) Error() string {
	return "replicaforward: read member records: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *MemberLookupError) Unwrap() error { return e.Err }

// RefusalDecodeError reports structured refusal metadata that does not
// decode: Key is missing or malformed.
type RefusalDecodeError struct {
	Key string
	Err error
}

// Error implements the error interface.
func (e *RefusalDecodeError) Error() string {
	msg := "replicaforward: refusal metadata key " + e.Key + " is missing or malformed"
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

// Unwrap returns the underlying error.
func (e *RefusalDecodeError) Unwrap() error { return e.Err }
