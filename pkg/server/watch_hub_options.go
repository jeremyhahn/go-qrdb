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

package server

import (
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// WatchHubOptions returns the hub options of a node's watch section:
// watch.buffer_size as every watcher's event buffer and watch.max_watchers as
// one watcher limit. Call it once per node and give the result to every hub
// the node builds, its own and its tenants' (TenantInfraConfig.WatchHub), so
// the cap bounds the node rather than each database. qrdbd, an sdk/go/node
// Node, the sdk/go/standalone server and the desktop's embedded server all
// build their hubs from it. Zero keeps the package
// default buffer and leaves the node unlimited.
func WatchHubOptions(cfg qrdbconfig.WatchConfig) []watch.HubOption {
	return []watch.HubOption{
		watch.WithEventBufferSize(cfg.BufferSize),
		watch.WithWatcherLimit(watch.NewWatcherLimit(cfg.MaxWatchers)),
	}
}
