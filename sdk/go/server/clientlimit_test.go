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

package server_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	sdkgo "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/server"
)

// TestClientRateLimitConfig_IsTheDaemonField: an embedder sets the limit
// once, on the node configuration, through the SDK's own name for it; the
// zero value applies no limit.
func TestClientRateLimitConfig_IsTheDaemonField(t *testing.T) {
	var cfg sdkgo.DaemonConfig
	if cfg.Transport.ClientRateLimit.Enabled() {
		t.Fatal("the zero configuration enabled the limit")
	}
	cfg.Transport.ClientRateLimit = server.ClientRateLimitConfig{RatePerSecond: 50, Burst: 100, TrustedProxies: []server.TrustedProxyConfig{{Address: "10.0.0.0/8", Header: "x-forwarded-for"}}}
	if !cfg.Transport.ClientRateLimit.Enabled() {
		t.Fatal("a configured limit is not enabled")
	}
	proxies, err := cfg.Transport.ClientRateLimit.ClientLimitProxies()
	if err != nil || len(proxies) != 1 || proxies[0].Prefix.String() != "10.0.0.0/8" || proxies[0].Header != "X-Forwarded-For" {
		t.Fatalf("ClientLimitProxies = %v, %v", proxies, err)
	}
}

// TestClientRateLimitedError_Matchable: a refusal surfaced in process is
// matched by the SDK's name for it and names the address, not a principal.
func TestClientRateLimitedError_Matchable(t *testing.T) {
	err := fmt.Errorf("serve: %w", &server.ClientRateLimitedError{Client: "192.0.2.1", RetryAfter: 2 * time.Second})
	var rl *server.ClientRateLimitedError
	if !errors.As(err, &rl) || rl.RetryAfter != 2*time.Second {
		t.Fatalf("errors.As = %v, %+v", err, rl)
	}
	if !strings.Contains(err.Error(), "192.0.2.1") {
		t.Fatalf("message %q does not name the client address", err)
	}
}
