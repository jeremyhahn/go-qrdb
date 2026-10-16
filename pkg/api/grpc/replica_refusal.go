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

package grpc

import (
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/api/errclass"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
)

// refusalDetail returns the structured refusal err carries (errclass.Refusal)
// -- a node that holds no replica of the database, a follower refusing a
// write, a conditional factor add whose condition failed -- as a
// google.rpc.ErrorInfo detail. Any other error carries none.
func refusalDetail(err error) *errdetails.ErrorInfo {
	reason, md, ok := errclass.Refusal(err)
	if !ok {
		return nil
	}
	return &errdetails.ErrorInfo{Domain: replicaforward.ErrorDomain, Reason: reason, Metadata: md}
}

// refusalFromStatus returns the reason and metadata of the replica refusal st
// carries, if any.
func refusalFromStatus(st *status.Status) (reason string, md map[string]string, ok bool) {
	for _, d := range st.Details() {
		if info, isInfo := d.(*errdetails.ErrorInfo); isInfo && info.GetDomain() == replicaforward.ErrorDomain {
			return info.GetReason(), info.GetMetadata(), true
		}
	}
	return "", nil, false
}
