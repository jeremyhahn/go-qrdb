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

//go:build clusterproof

package workload

import (
	"context"
	"math/rand/v2"
	"time"
)

// backoff is the pause a client takes after each failure in a row: a failed
// dial, or an operation that did not succeed. The step starts at base,
// doubles with every failure up to limit, and each pause is drawn uniformly
// from the upper half of its step (equal jitter), so clients that failed
// together do not retry together. reset, after a success, starts over.
type backoff struct {
	base, limit time.Duration
	rng         *rand.Rand
	sleep       func(context.Context, time.Duration)
	step        time.Duration // the next failure's step; zero means base
}

func newBackoff(base, limit time.Duration, rng *rand.Rand, sleep func(context.Context, time.Duration)) *backoff {
	return &backoff{base: base, limit: limit, rng: rng, sleep: sleep}
}

// next returns the pause after one more failure and grows the step.
func (b *backoff) next() time.Duration {
	step := b.step
	if step == 0 {
		step = b.base
	}
	b.step = min(step*2, b.limit)
	half := step / 2
	return half + time.Duration(b.rng.Int64N(int64(step-half)+1))
}

// reset starts the next failure over from base.
func (b *backoff) reset() { b.step = 0 }

// wait pauses for next through sleep, which returns early when ctx ends.
func (b *backoff) wait(ctx context.Context) { b.sleep(ctx, b.next()) }

// sleepContext pauses for d or until ctx is done, whichever comes first.
func sleepContext(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
