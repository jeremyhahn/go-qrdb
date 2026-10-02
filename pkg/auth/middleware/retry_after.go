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

package middleware

import (
	"errors"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// authRetryAfterRules are the authentication and authorization refusals that
// say how long the caller must wait before a retry can succeed, each reading
// its own delay. pkg/api/errclass.RetryAfter consults RetryAfter as one of its
// rules, so a transport that renders a delay from errclass renders these too;
// the renderers in this package (REST Retry-After, gRPC RetryInfo) read this
// list directly because errclass imports this package.
var authRetryAfterRules = []func(error) (time.Duration, bool){
	// A request over the caller's per-identity rate limit.
	func(err error) (time.Duration, bool) {
		var e *RateLimitedError
		if errors.As(err, &e) {
			return e.RetryAfter, true
		}
		return 0, false
	},
	// A tenant SO PIN check refused while that tenant's lockout is engaged.
	func(err error) (time.Duration, bool) {
		var e *auth.SOPINLockedError
		if errors.As(err, &e) {
			return e.RetryAfter, true
		}
		return 0, false
	},
}

// RetryAfter reports how long an authentication or authorization refusal says
// its caller must wait before a retry can succeed, and whether it says so.
func RetryAfter(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	for _, rule := range authRetryAfterRules {
		if d, ok := rule(err); ok {
			return d, true
		}
	}
	return 0, false
}

// RetryAfterSeconds renders a retry delay in whole seconds, the delay-seconds
// form of RFC 9110's Retry-After: rounded up, and never below one, so a caller
// that honours it is never early. Every transport that speaks whole seconds
// (REST, MCP) renders through it.
func RetryAfterSeconds(d time.Duration) int64 {
	return max(int64((d+time.Second-1)/time.Second), 1)
}
