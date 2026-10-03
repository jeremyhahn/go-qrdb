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

package clusterseal

import (
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// Streams is the registry every long-lived stream on this node subscribes
// to: watches on every transport, and the etcd watch and lease keep-alive
// streams. A seal ends the subscriptions of the sealed barrier's data; a
// cut-off ends them all. An ended or released subscription is forgotten.
//
// The zero Streams is not usable; build one with NewStreams.
type Streams struct {
	mu   sync.Mutex
	subs map[*Subscription]struct{}
}

// NewStreams returns an empty registry.
func NewStreams() *Streams {
	return &Streams{subs: make(map[*Subscription]struct{})}
}

// Subscription is one open stream's registration. Its Done channel closes
// when a seal or a cut-off ends the stream, and Cause then reports why.
type Subscription struct {
	streams  *Streams
	tenantID string
	database string
	// anyDatabase makes a seal of any of the tenant's databases end the
	// subscription, for a stream that is not bound to one database.
	anyDatabase bool

	done  chan struct{}
	once  sync.Once
	cause error // written once, before done closes
}

// Subscribe registers a stream on (tenantID, database). The caller must
// Release it when the stream ends for any other reason.
func (s *Streams) Subscribe(tenantID, database string) *Subscription {
	sub := &Subscription{streams: s, tenantID: tenantID, database: database, done: make(chan struct{})}
	s.mu.Lock()
	s.subs[sub] = struct{}{}
	s.mu.Unlock()
	return sub
}

// Done closes when the stream must end.
func (sub *Subscription) Done() <-chan struct{} { return sub.done }

// Cause reports why the stream was ended, or nil while it has not been.
func (sub *Subscription) Cause() error {
	select {
	case <-sub.done:
		return sub.cause
	default:
		return nil
	}
}

// Release forgets the subscription without ending it. It is idempotent.
func (sub *Subscription) Release() {
	sub.streams.mu.Lock()
	delete(sub.streams.subs, sub)
	sub.streams.mu.Unlock()
}

// end closes the subscription with cause; the first cause wins.
func (sub *Subscription) end(cause error) {
	sub.once.Do(func() {
		sub.cause = cause
		close(sub.done)
	})
}

// EndAll ends every stream on this node.
func (s *Streams) EndAll(cause error) {
	s.endMatching(cause, func(*Subscription) bool { return true })
}

// EndTenant ends every stream on any of tenantID's databases.
func (s *Streams) EndTenant(tenantID string, cause error) {
	s.endMatching(cause, func(sub *Subscription) bool { return sub.tenantID == tenantID })
}

// EndDatabase ends every stream on tenantID's database, and every stream
// subscribed to all of tenantID's databases.
func (s *Streams) EndDatabase(tenantID, database string, cause error) {
	s.endMatching(cause, func(sub *Subscription) bool {
		return sub.tenantID == tenantID && (sub.anyDatabase || sub.database == database)
	})
}

// Len reports how many subscriptions are registered.
func (s *Streams) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs)
}

func (s *Streams) endMatching(cause error, match func(*Subscription) bool) {
	s.mu.Lock()
	var ending []*Subscription
	for sub := range s.subs {
		if match(sub) {
			ending = append(ending, sub)
			delete(s.subs, sub)
		}
	}
	s.mu.Unlock()
	for _, sub := range ending {
		sub.end(cause)
	}
}

// SubscribeServing registers a stream on (tenantID, database) and only then
// asks refuses whether the stream may open. It is the one way a stream opens
// on this node, because the order is what makes a seal unmissable: a seal
// closes its gate -- the published record, the node's refusal -- BEFORE it
// ends streams. A seal whose End* ran before this subscription had already
// closed the gate, so refuses sees it and the open is refused; a seal whose
// End* runs after it ends this subscription. Checking first and subscribing
// after leaves a window in which a seal lands between the two and the stream
// lives forever.
//
// refuses returns the typed refusal the transport answers, or nil while the
// stream may open; nil refuses checks nothing. On a refusal the subscription
// is released and the refusal returned.
func (s *Streams) SubscribeServing(tenantID, database string, refuses func() error) (*Subscription, error) {
	sub := s.Subscribe(tenantID, database)
	if refuses != nil {
		if err := refuses(); err != nil {
			sub.Release()
			return nil, err
		}
	}
	return sub, nil
}

// SubscribeTenantServing is SubscribeServing for a stream that is not bound
// to one database -- a SQL session, which can switch databases at any
// statement: a seal of the node, of tenantID, or of ANY of tenantID's
// databases ends it, as does a cut-off.
func (s *Streams) SubscribeTenantServing(tenantID string, refuses func() error) (*Subscription, error) {
	sub := &Subscription{streams: s, tenantID: tenantID, anyDatabase: true, done: make(chan struct{})}
	s.mu.Lock()
	s.subs[sub] = struct{}{}
	s.mu.Unlock()
	if refuses != nil {
		if err := refuses(); err != nil {
			sub.Release()
			return nil, err
		}
	}
	return sub, nil
}

// GuardWatcher opens a watch on (tenantID, database) that a seal or a
// cut-off ends: it subscribes and checks refuses (SubscribeServing), then
// opens the watch with open, and returns a watcher that ends with the
// subscription's cause and releases the subscription when closed. Every
// transport's watch -- the network listeners through the server's service
// registry, and the embedded transport's own -- opens through it.
func (s *Streams) GuardWatcher(tenantID, database string, refuses func() error, open func() (watch.Watcher, error)) (watch.Watcher, error) {
	sub, err := s.SubscribeServing(tenantID, database, refuses)
	if err != nil {
		return nil, err
	}
	w, err := open()
	if err != nil {
		sub.Release()
		return nil, err
	}
	return &guardedWatcher{Guarded: watch.NewGuarded(w, sub.Done(), sub.Cause), sub: sub}, nil
}

// guardedWatcher is a watch.Guarded whose Close also forgets its
// subscription.
type guardedWatcher struct {
	*watch.Guarded
	sub *Subscription
}

// Close ends the watch and forgets its subscription.
func (w *guardedWatcher) Close() error {
	w.sub.Release()
	return w.Guarded.Close()
}
