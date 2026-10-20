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
	"sync/atomic"

	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"
)

// LeaderEvents relays a quicraft host's leader-readiness events to the
// tenant Raft factory. A host's event listener is fixed when the host is
// built, before BuildTenantInfra builds the factory that consumes the events,
// so the host builder installs Listener and BuildTenantInfra binds the
// factory (TenantInfraConfig.LeaderEvents). An event delivered before the
// bind is dropped: nothing that consumes it exists yet.
type LeaderEvents struct {
	ready atomic.Pointer[func(quicraftconfig.LeaderInfo)]
}

// NewLeaderEvents returns a relay with nothing bound.
func NewLeaderEvents() *LeaderEvents {
	return &LeaderEvents{}
}

// Listener returns the host event listener that feeds the relay. It sets only
// OnLeaderReady, which quicraft delivers on its event dispatcher goroutine.
func (e *LeaderEvents) Listener() *quicraftconfig.EventListener {
	return &quicraftconfig.EventListener{OnLeaderReady: e.leaderReady}
}

// Bind directs every later leader-readiness event to fn, replacing any
// earlier binding.
func (e *LeaderEvents) Bind(fn func(quicraftconfig.LeaderInfo)) {
	e.ready.Store(&fn)
}

func (e *LeaderEvents) leaderReady(info quicraftconfig.LeaderInfo) {
	if fn := e.ready.Load(); fn != nil {
		(*fn)(info)
	}
}
