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

// countedWatch marks every watch it opens with watch.CountedContext, so the
// watch holds a slot of watch.max_watchers on the hub that serves it.
type countedWatch struct {
	WatchServicer
}

// NewCountedWatch wraps svc so every watch opened through it counts against
// watch.max_watchers. A transport wraps the WatchServicer it serves clients
// with; the node's own watchers (tenant lifecycle, placement, admin records)
// reach the hub unwrapped, so a full cap refuses clients and never starves
// the node. A nil svc stays nil, so "watch not configured" is preserved.
func NewCountedWatch(svc WatchServicer) WatchServicer {
	if svc == nil {
		return nil
	}
	return &countedWatch{WatchServicer: svc}
}

func (c *countedWatch) Watch(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error) {
	return c.WatchServicer.Watch(watch.CountedContext(ctx), key, opts...)
}

func (c *countedWatch) WatchPrefix(ctx context.Context, prefix string, opts ...watch.WatchOption) (watch.Watcher, error) {
	return c.WatchServicer.WatchPrefix(watch.CountedContext(ctx), prefix, opts...)
}

func (c *countedWatch) WatchEntity(ctx context.Context, req *WatchEntityRequest) (<-chan WatchEntityEvent, error) {
	return c.WatchServicer.WatchEntity(watch.CountedContext(ctx), req)
}

func (c *countedWatch) WatchEntityType(ctx context.Context, req *WatchEntityTypeRequest) (<-chan WatchEntityEvent, error) {
	return c.WatchServicer.WatchEntityType(watch.CountedContext(ctx), req)
}
