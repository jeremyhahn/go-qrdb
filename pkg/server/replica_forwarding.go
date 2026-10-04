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
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
)

// WithReplicaForwarding makes every network listener forward a request for
// a database this node holds no replica of to a node that does, as the
// caller, through l; one forward is bounded by timeout (zero is
// replicaforward.DefaultTimeout). A target's listener certificate is
// verified against the WithCACertPool pool, the one REST leader forwarding
// trusts; forwarding needs it, and a server given no pool forwards nothing.
// Without either, every listener answers the typed refusal.
func WithReplicaForwarding(l *replicaforward.Locator, timeout time.Duration) ServerOption {
	return func(o *serverOptions) {
		o.replicaLocator = l
		o.replicaTimeout = timeout
	}
}

// replicaForwarding returns the listeners' forwarding configuration, and
// false when this server does not forward: it was not asked to, or it has no
// pool to verify a target with.
func (s *Server) replicaForwarding() (replicaforward.Config, bool) {
	if s.opts.replicaLocator == nil {
		return replicaforward.Config{}, false
	}
	if s.opts.caCertPool == nil {
		s.forwardingUntrusted.Do(func() {
			s.logger.Warn("replica forwarding off: no CA pool to verify a replica with (server.WithCACertPool); a non-replica answers the refusal")
		})
		return replicaforward.Config{}, false
	}
	return replicaforward.Config{
		Locator: s.opts.replicaLocator,
		RootCAs: s.opts.caCertPool,
		Timeout: s.opts.replicaTimeout,
	}, true
}
