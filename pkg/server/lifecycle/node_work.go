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

package lifecycle

import (
	"context"
	"sync"
)

// nodeWork is the work a Node runs on its own behalf rather than for a
// caller, such as completing a pending admin when the node gains a tenant
// home group's leadership. Its context lives as long as the node: stop
// cancels it and waits for every piece of work to return, so none outlives
// the node. The zero value is ready to use.
type nodeWork struct {
	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc

	// mu orders start's wg.Add against stop's wg.Wait: once stopped is set
	// no work is added, so the wait cannot miss one.
	mu      sync.Mutex
	stopped bool
	wg      sync.WaitGroup
}

func (w *nodeWork) init() {
	w.once.Do(func() {
		w.ctx, w.cancel = context.WithCancel(context.Background())
	})
}

// context returns the context stop cancels.
func (w *nodeWork) context() context.Context {
	w.init()
	return w.ctx
}

// start runs fn on its own goroutine with the node's context and reports
// whether it did; once stop has begun it runs nothing.
func (w *nodeWork) start(fn func(ctx context.Context)) bool {
	w.init()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped {
		return false
	}
	w.wg.Go(func() { fn(w.ctx) })
	return true
}

// stop cancels the node's work and returns once every piece has returned.
// It is safe to call more than once.
func (w *nodeWork) stop() {
	w.init()
	w.mu.Lock()
	w.stopped = true
	w.mu.Unlock()
	w.cancel()
	w.wg.Wait()
}
