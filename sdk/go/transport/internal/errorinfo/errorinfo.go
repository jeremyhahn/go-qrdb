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

// Package errorinfo reads the structured refusal a gRPC status carries, for
// the transports that speak gRPC (gRPC over TCP and the Unix socket).
package errorinfo

import (
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/status"
)

// Refusal returns the reason and metadata of the first google.rpc.ErrorInfo
// detail in st whose domain is domain, or empty values when st carries none.
// A detail in another domain is some other system's vocabulary and is skipped.
func Refusal(st *status.Status, domain string) (string, map[string]string) {
	for _, detail := range st.Details() {
		info, ok := detail.(*errdetails.ErrorInfo)
		if !ok || info.GetDomain() != domain {
			continue
		}
		return info.GetReason(), info.GetMetadata()
	}
	return "", nil
}
