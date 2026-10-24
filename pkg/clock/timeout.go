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

package clock

import (
	"context"
	"sync/atomic"
	"time"
)

// WithTimeout returns a copy of parent that ends once d has passed on c, as
// context.WithTimeout does on the runtime's clock: past its deadline the
// context is done and its Err is context.DeadlineExceeded. It ends earlier,
// with parent's error, when parent does, and with context.Canceled when the
// returned cancel is called. The caller must call cancel to release the timer.
//
// On Real, or a nil c, it is context.WithTimeout itself. On any other Clock it
// arms a Timer from c, so a test's clocktest.Fake decides when the deadline
// passes and a loaded machine cannot end the call early.
func WithTimeout(parent context.Context, c Clock, d time.Duration) (context.Context, context.CancelFunc) {
	if _, isReal := c.(realClock); isReal || c == nil {
		return context.WithTimeout(parent, d)
	}
	ctx := &timeoutContext{Context: parent, deadline: c.Now().Add(d), done: make(chan struct{})}
	if pd, ok := parent.Deadline(); ok && pd.Before(ctx.deadline) {
		ctx.deadline = pd
	}
	if d <= 0 {
		ctx.end(context.DeadlineExceeded)
		return ctx, func() {}
	}
	timer := c.NewTimer(d)
	go func() {
		defer timer.Stop()
		select {
		case <-timer.C():
			ctx.end(context.DeadlineExceeded)
		case <-parent.Done():
			ctx.end(parent.Err())
		case <-ctx.done:
		}
	}()
	return ctx, func() { ctx.end(context.Canceled) }
}

// timeoutContext is the context WithTimeout returns on a Clock other than
// Real. Values come from the parent; Done and Err are its own, so a context
// derived from it sees the deadline as context.DeadlineExceeded.
type timeoutContext struct {
	context.Context
	deadline time.Time
	done     chan struct{}
	err      atomic.Pointer[error]
}

func (c *timeoutContext) Deadline() (time.Time, bool) { return c.deadline, true }

func (c *timeoutContext) Done() <-chan struct{} { return c.done }

// Err reports why the context ended, and nil until Done is closed.
func (c *timeoutContext) Err() error {
	select {
	case <-c.done:
		return *c.err.Load()
	default:
		return nil
	}
}

// end records err as the reason the context ended and closes Done. Only the
// first call has any effect.
func (c *timeoutContext) end(err error) {
	if c.err.CompareAndSwap(nil, &err) {
		close(c.done)
	}
}
