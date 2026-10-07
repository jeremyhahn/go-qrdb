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

package embedded

import (
	"context"
	"strconv"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// HolderActLimitFunc decides whether one more anonymous share-holder act may
// proceed now, for the bucket key named. It reports false and the wait before
// the next would be admitted when the bucket is empty. sdk/go/node installs
// lifecycle.QuorumLimit -- the same throttle the ceremony applies to quorum
// challenges -- under a key naming this node, so nodes in one process never
// share a bucket. anonymous is always true here: a claim carrying an
// identity never reaches limitHolderAct (see requireIdentity).
type HolderActLimitFunc func(key string, anonymous bool) (allowed bool, retryAfter time.Duration)

// WithHolderActLimiter installs the throttle on share-holder claims made with
// no identity on the context. requireIdentity rejects such a claim outright
// once an AuthMiddleware is installed (WithAuthMiddleware), so the claim
// reaches here with no identity only when none is: an embedded composition
// with auth disabled, where the holder's signed claim nonce is its own
// authorization, has nothing else bounding how fast a caller can probe
// holder usernames and roster entries in-process. A claim that does carry an
// identity is rate-limited by the auth middleware instead
// (pkg/auth/middleware). Nil (the default) installs no throttle.
func WithHolderActLimiter(fn HolderActLimitFunc) Option {
	return func(t *Transport) {
		t.holderActLimiter = fn
	}
}

// HolderActRateLimitedError refuses an anonymous share-holder claim over its
// scope's throttle.
type HolderActRateLimitedError struct {
	// Operation is the refused operation, e.g. "ShareClaimBegin".
	Operation string
	// RetryAfter is how long until the next claim would be admitted.
	RetryAfter time.Duration
}

// Error names the operation and the wait.
func (e *HolderActRateLimitedError) Error() string {
	return "embedded: " + e.Operation + " rate limited; retry after " + e.RetryAfter.String()
}

// limitHolderAct charges an anonymous share-holder claim to its scope's
// bucket. scope names the barrier the claim is for. It is a no-op for a call
// carrying an identity, and when no throttle is installed.
func (t *Transport) limitHolderAct(ctx context.Context, op, scope string) error {
	if t.holderActLimiter == nil || auth.IdentityFromContext(ctx) != nil {
		return nil
	}
	if ok, retry := t.holderActLimiter(holderActKeyPrefix+scope, true); !ok {
		return &HolderActRateLimitedError{Operation: op, RetryAfter: retry}
	}
	return nil
}

// holderActKeyPrefix keeps the embedded claim buckets apart from the
// ceremony's own quorum-challenge buckets, which share the same limiter
// engine (lifecycle.QuorumLimit) when sdk/go/node installs both.
const holderActKeyPrefix = "embedded-holder-claim\x00"

// barrierScopeKey, tenantScopeKey and databaseScopeKey name a claim's
// barrier: one bucket per barrier, shared by every anonymous caller of it.
// tenantScopeKey normalizes: a claim naming "" and one naming the system
// tenant are the same barrier and must share a bucket.
func barrierScopeKey(domain string) string { return "system/" + domain }
func tenantScopeKey(tenantID string) string {
	return "tenant/" + strconv.Quote(tenantid.Normalize(tenantID))
}
func databaseScopeKey(ctx context.Context, name string) string {
	return "database/" + strconv.Quote(tenant.FromContext(ctx)) + "/" + strconv.Quote(name)
}
