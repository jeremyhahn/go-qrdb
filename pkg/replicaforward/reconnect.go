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

import (
	"context"
	"math/rand/v2"
	"time"
)

// The schedule a forward reconnects to a target on, on every transport: a
// gRPC channel's backoff, which the gRPC forwarder's connections use as is.
const (
	// ReconnectBaseDelay is the wait after the first failed attempt.
	ReconnectBaseDelay = 100 * time.Millisecond
	// ReconnectMultiplier grows the wait after each further failure.
	ReconnectMultiplier = 1.6
	// ReconnectJitter randomizes each wait by up to this fraction either
	// way, so forwards waiting on one target do not reconnect in step.
	ReconnectJitter = 0.2
	// ReconnectMaxDelay caps the wait.
	ReconnectMaxDelay = 2 * time.Second
)

// Reconnect makes attempts to reach a target until one reaches it, ctx
// ends, or budget has passed since the first, waiting between attempts on
// the reconnect schedule. A target that is restarting, or that fails a
// handshake under load, is thereby waited on as a gRPC channel waits on a
// reconnecting connection, rather than counted unreachable at its first
// failure. An attempt reports false only when nothing of the request left
// this node, so making it again is safe. Reconnect reports whether an
// attempt reached the target.
func Reconnect(ctx context.Context, budget time.Duration, attempt func() bool) bool {
	deadline := time.Now().Add(budget)
	delay := ReconnectBaseDelay
	for {
		if attempt() {
			return true
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || ctx.Err() != nil {
			return false
		}
		timer := time.NewTimer(min(jittered(delay), remaining))
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
		delay = min(time.Duration(float64(delay)*ReconnectMultiplier), ReconnectMaxDelay)
	}
}

// jittered spreads d by up to ReconnectJitter either way.
func jittered(d time.Duration) time.Duration {
	// #nosec G404 -- backoff jitter spreads reconnects; it is not a secret.
	return time.Duration(float64(d) * (1 + ReconnectJitter*(2*rand.Float64()-1)))
}
