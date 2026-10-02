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

package errclass

import (
	"errors"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// retryAfterRules are the refusals that say how long the caller must wait
// before a retry can succeed, each reading its own delay. Every transport
// renders the delay from RetryAfter -- REST as Retry-After, gRPC and the Unix
// socket as a google.rpc.RetryInfo status detail, MCP in the error envelope,
// QUIC in the error frame's google.rpc.Status -- so one list decides which
// refusals carry one on all of them.
var retryAfterRules = []func(error) (time.Duration, bool){
	// A throttled ceremony-sign-csr.
	func(err error) (time.Duration, bool) {
		var e *service.SignCSRThrottledError
		if errors.As(err, &e) {
			return e.RetryAfter, true
		}
		return 0, false
	},
	// A quorum challenge request or share submission over the caller's
	// throttle.
	func(err error) (time.Duration, bool) {
		var e *ceremony.QuorumRateLimitedError
		if errors.As(err, &e) {
			return e.RetryAfter, true
		}
		return 0, false
	},
	// An SO PIN check refused while the node's SO PIN lockout is engaged.
	func(err error) (time.Duration, bool) {
		var e *pin.SOPINLockedError
		if errors.As(err, &e) {
			return e.RetryAfter, true
		}
		return 0, false
	},
	// The authentication and authorization refusals that name a delay: a
	// request over the caller's rate limit, a tenant SO PIN lockout.
	middleware.RetryAfter,
}

// RetryAfter reports how long err says its caller must wait before a retry
// can succeed, and whether it says so at all.
func RetryAfter(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	for _, rule := range retryAfterRules {
		if d, ok := rule(err); ok {
			return d, true
		}
	}
	return 0, false
}
