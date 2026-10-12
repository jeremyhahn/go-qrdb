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

package replicaforward

import (
	"crypto/x509"
	"time"
)

// DefaultTimeout bounds one forward, connection included, when Config sets
// none: the cluster.replica_forwarding_timeout default.
const DefaultTimeout = 10 * time.Second

// Config is one listener's replica forwarding. Each transport takes it
// through its WithReplicaForwarding option; a listener without one never
// forwards, and answers the refusal.
type Config struct {
	// Locator resolves the targets.
	Locator *Locator

	// RootCAs verifies a target's listener certificate: the cluster CA, and
	// on a node with operator-supplied TLS the operator's peer CA. The node
	// presents its own listener certificate as its client certificate.
	RootCAs *x509.CertPool

	// Timeout bounds one forward, connection included. Zero is
	// DefaultTimeout.
	Timeout time.Duration
}

// ForwardTimeout returns the bound on one forward.
func (c Config) ForwardTimeout() time.Duration {
	if c.Timeout <= 0 {
		return DefaultTimeout
	}
	return c.Timeout
}

// ConnectTimeout returns the bound on reaching one target: half the
// forward's, so a target that never answers leaves time for the one retry.
// A target whose connection fails within it is reconnected to (Reconnect)
// until it passes.
func (c Config) ConnectTimeout() time.Duration {
	return c.ForwardTimeout() / 2
}
