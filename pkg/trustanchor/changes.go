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

package trustanchor

import "sync/atomic"

// Changes fans one "a tenant's anchors changed" signal out to any number of
// subscribers, so the component that builds the Registry and the components
// that cache what it holds (trust pools) need not exist in a fixed order:
// wrap the registry with Observe(r, c.Notify) when building it, and have each
// cache Subscribe whenever it is built. The zero value is ready to use and
// it is safe for concurrent use.
type Changes struct {
	subs atomic.Pointer[[]func()]
}

// Subscribe registers fn to run on every Notify. A nil fn is ignored.
func (c *Changes) Subscribe(fn func()) {
	if fn == nil {
		return
	}
	for {
		old := c.subs.Load()
		var next []func()
		if old != nil {
			next = make([]func(), len(*old), len(*old)+1)
			copy(next, *old)
		}
		next = append(next, fn)
		if c.subs.CompareAndSwap(old, &next) {
			return
		}
	}
}

// Notify runs every subscriber, in subscription order.
func (c *Changes) Notify() {
	subs := c.subs.Load()
	if subs == nil {
		return
	}
	for _, fn := range *subs {
		fn()
	}
}
