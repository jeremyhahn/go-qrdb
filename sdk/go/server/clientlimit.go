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
	"github.com/jeremyhahn/go-qrdb/pkg/config"
)

// ClientRateLimitConfig is the pre-authentication client-address rate limit
// (go-qrdb#1197): the config.Config field Transport.ClientRateLimit, which an
// embedder sets once on the node's configuration. Every transport -- REST,
// gRPC (gRPC-Web included), native QUIC, MCP and the Unix socket -- shares
// one limiter built from it. The zero value applies no limit.
type ClientRateLimitConfig = config.ClientRateLimitConfig

// TrustedProxyConfig is one ClientRateLimitConfig.TrustedProxies entry: a
// proxy's address or CIDR prefix and the one header field it appends to,
// "forwarded" (RFC 7239) or "x-forwarded-for". A hop the proxy reports is
// read from that field only.
type TrustedProxyConfig = config.TrustedProxyConfig

// ClientRateLimitedError refuses a request whose client address has
// exhausted its bucket, before the request is authenticated. Each transport
// renders it as its rate-limit status: 429 with Retry-After on REST and MCP,
// ResourceExhausted with a RetryInfo detail on gRPC and the Unix socket,
// RESOURCE_EXHAUSTED on native QUIC.
type ClientRateLimitedError = clientlimit.RateLimitedError
