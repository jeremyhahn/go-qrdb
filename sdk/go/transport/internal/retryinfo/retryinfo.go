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

// Package retryinfo reads the retry delay a gRPC status names, for the
// transports that speak gRPC (gRPC over TCP and the Unix socket).
package retryinfo

import (
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/status"
)

// Delay returns the delay of the first google.rpc.RetryInfo detail st
// carries, and zero when it carries none or one whose delay is not valid.
func Delay(st *status.Status) time.Duration {
	for _, detail := range st.Details() {
		ri, ok := detail.(*errdetails.RetryInfo)
		if !ok {
			continue
		}
		if ri.GetRetryDelay().CheckValid() != nil {
			return 0
		}
		return ri.GetRetryDelay().AsDuration()
	}
	return 0
}
