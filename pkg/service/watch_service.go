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

package service

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// WatchServicer provides real-time change notifications for keys in the
// MVCC key-value store. Events are delivered in revision order, and
// watchers can resume from a historical revision to replay missed events.
type WatchServicer interface {
	// Watch creates a watcher that delivers events for the specified
	// key. The watcher remains active until Close is called or the
	// context is canceled.
	Watch(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error)

	// WatchPrefix creates a watcher that delivers events for all keys
	// matching the given prefix. The watcher remains active until
	// Close is called or the context is canceled.
	WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error)
}

// WatchHub is the interface required by the WatchService to create and
// manage watchers. This interface decouples the service layer from the
// watch package to prevent import cycles.
type WatchHub interface {
	// Watch creates a watcher for the specified key.
	Watch(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error)

	// WatchPrefix creates a watcher for all keys matching the prefix.
	WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error)
}

// WatchService implements WatchServicer by delegating to a WatchHub.
type WatchService struct {
	hub WatchHub
}

// NewWatchService creates a new WatchServicer backed by the given hub.
func NewWatchService(hub WatchHub) WatchServicer {
	return &WatchService{hub: hub}
}

// Watch creates a watcher that delivers events for the specified key.
func (s *WatchService) Watch(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error) {
	return s.hub.Watch(ctx, key, opts...)
}

// WatchPrefix creates a watcher that delivers events for all keys
// matching the given prefix.
func (s *WatchService) WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error) {
	return s.hub.WatchPrefix(ctx, prefix, opts...)
}
