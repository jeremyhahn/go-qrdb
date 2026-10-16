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

// Package refusalinfo rebuilds the typed error a gRPC status carries as a
// structured refusal, for the transports that speak gRPC status (gRPC and
// the Unix socket).
package refusalinfo

import (
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ErrorDomain is the google.rpc.ErrorInfo domain of qrdb's structured
// refusals; the server's is replicaforward.ErrorDomain.
const ErrorDomain = "qrdb.v1"

// Error returns the typed error st's qrdb ErrorInfo detail names, or nil
// when st carries none the client decodes.
func Error(st *status.Status) error {
	for _, detail := range st.Details() {
		if info, ok := detail.(*errdetails.ErrorInfo); ok && info.GetDomain() == ErrorDomain {
			return transport.RefusalError(info.GetReason(), info.GetMetadata())
		}
	}
	return nil
}
