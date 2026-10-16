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

// Package sealedinfo recognises the sealed-barrier refusal a gRPC status
// carries, for the transports that speak gRPC (gRPC over TCP and the Unix
// socket).
package sealedinfo

import (
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
)

// Sealed reports whether st is the sealed node's refusal: codes.Unavailable
// carrying a google.rpc.ErrorInfo detail in the qrdb domain with reason
// SEALED. The code alone is not enough — Unavailable also means a node that
// is draining or unreachable — so the detail is what makes it this error.
func Sealed(st *status.Status) bool {
	if st.Code() != codes.Unavailable {
		return false
	}
	for _, detail := range st.Details() {
		info, ok := detail.(*errdetails.ErrorInfo)
		if !ok {
			continue
		}
		if info.GetDomain() == sealedwindow.ErrorDomain && info.GetReason() == sealedwindow.ReasonSealed {
			return true
		}
	}
	return false
}
