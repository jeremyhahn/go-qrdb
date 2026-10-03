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

package watch

import "sync"

// Guarded forwards an inner watcher's events until the inner watcher ends,
// the Guarded is closed, or ended closes. In the last case it closes the
// inner watcher and reports cause() from Err, so whoever relays the stream
// sends the refusal as its terminal frame rather than a clean close.
//
// It is the one implementation of "a stream that outlives the request that
// opened it ends when what it reads stops serving": a dropped or sealed
// database, and a cluster seal or cut-off reaching the stream's barrier.
type Guarded struct {
	inner  Watcher
	events chan WatchResponse
	stop   chan struct{}
	once   sync.Once
	errMu  sync.Mutex
	err    error
	cause  func() error
}

var _ Watcher = (*Guarded)(nil)

// NewGuarded starts forwarding inner's events until ended closes; cause
// supplies the error Err reports once it has.
func NewGuarded(inner Watcher, ended <-chan struct{}, cause func() error) *Guarded {
	w := &Guarded{
		inner:  inner,
		events: make(chan WatchResponse),
		stop:   make(chan struct{}),
		cause:  cause,
	}
	go w.forward(ended)
	return w
}

// forward relays events until the inner watcher ends, the watcher is closed,
// or ended closes.
func (w *Guarded) forward(ended <-chan struct{}) {
	defer close(w.events)
	for {
		select {
		case <-w.stop:
			return
		case <-ended:
			w.end()
			return
		case resp, ok := <-w.inner.Events():
			if !ok {
				return
			}
			select {
			case w.events <- resp:
			case <-w.stop:
				return
			case <-ended:
				w.end()
				return
			}
		}
	}
}

// end records the cause and closes the inner watcher.
func (w *Guarded) end() {
	w.errMu.Lock()
	w.err = w.cause()
	w.errMu.Unlock()
	_ = w.inner.Close()
}

// Events returns the forwarded events; the channel closes when the watch
// ends.
func (w *Guarded) Events() <-chan WatchResponse { return w.events }

// WatchID returns the inner watcher's ID.
func (w *Guarded) WatchID() int64 { return w.inner.WatchID() }

// Close ends the watch.
func (w *Guarded) Close() error {
	w.once.Do(func() { close(w.stop) })
	return w.inner.Close()
}

// Err returns the cause when ended closed the watch, and the inner watcher's
// error otherwise.
func (w *Guarded) Err() error {
	w.errMu.Lock()
	defer w.errMu.Unlock()
	if w.err != nil {
		return w.err
	}
	return w.inner.Err()
}
