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

package clienttrust

import "crypto/tls"

// ReassertListenerTLS clones base, applies the listener-specific override to
// the clone, and -- if base already carries a GetConfigForClient hook
// (installed by Pool.Wire, shared with every other client-facing listener)
// -- wraps it so the SAME override is reasserted on whatever config the
// hook returns.
//
// Without this, a listener that clones the shared config and tunes ONE
// field (REST/gRPC/Unix/MCP's TLS 1.3 floor, QUIC's exclusive ALPN
// protocol) loses that tuning on any connection that takes the dynamic
// (widened Key B) path: tls.Config.Clone() copies the hook's closure, not
// its effect, and the closure still clones from the config captured when
// Wire was called, before this override ever ran -- proven as a TLS 1.2
// downgrade on REST/gRPC/Unix/MCP and an ALPN mismatch on QUIC (go-qrdb#560
// review, B2-3).
//
// The override is applied to a clone of what the hook returns, never to the
// returned config itself: Pool.Wire's hook hands back the shared config --
// or a composer's own per-connection config -- unchanged whenever no
// widening applies, and every listener sharing it would otherwise race to
// write its own tuning into it, the last writer's ALPN or floor leaking
// onto every other listener's connections (go-qrdb#741).
//
// Every WithTLS that clones a *tls.Config already carrying a dynamic hook
// must call this instead of cloning and mutating directly.
func ReassertListenerTLS(base *tls.Config, apply func(*tls.Config)) *tls.Config {
	clone := base.Clone()
	apply(clone)
	if dynamic := clone.GetConfigForClient; dynamic != nil {
		clone.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			got, err := dynamic(hello)
			if err != nil || got == nil {
				return got, err
			}
			own := got.Clone()
			apply(own)
			return own, nil
		}
	}
	return clone
}
