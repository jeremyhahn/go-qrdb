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
	"github.com/jeremyhahn/go-qrdb/pkg/clientlimit"
	"github.com/jeremyhahn/go-qrdb/pkg/ratelimit"
)

// openClientLimiter builds the ONE pre-authentication client-address
// limiter transport.client_rate_limit configures (go-qrdb#1197). Every
// transport this server starts is handed the same Limiter, so an address has
// one budget across REST, gRPC, QUIC, MCP and the Unix socket rather than
// one per protocol. An unset block leaves it nil, which every transport
// treats as "no limit".
func (s *Server) openClientLimiter() error {
	cfg := s.config.Transport.ClientRateLimit
	if s.clientLimit != nil || !cfg.Enabled() {
		return nil
	}
	proxies, err := cfg.ClientLimitProxies()
	if err != nil {
		return err
	}
	trusted := make([]clientlimit.TrustedProxy, len(proxies))
	for i, p := range proxies {
		trusted[i] = clientlimit.TrustedProxy{Prefix: p.Prefix, Header: p.Header}
	}
	l, err := clientlimit.New(clientlimit.Config{
		Policy:         ratelimit.Policy{RatePerSecond: cfg.RatePerSecond, Burst: cfg.Burst},
		TrustedProxies: trusted,
	})
	if err != nil {
		return err
	}
	s.clientLimit = l
	return nil
}

// closeClientLimiter releases the limiter's bucket reaper once no transport
// can consult it any more.
func (s *Server) closeClientLimiter() {
	if s.clientLimit == nil {
		return
	}
	if err := s.clientLimit.Close(); err != nil {
		s.logger.Warn("client rate limiter close failed", "error", err)
	}
	s.clientLimit = nil
}
