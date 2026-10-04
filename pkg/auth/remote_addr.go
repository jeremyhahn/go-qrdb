// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package auth

import "context"

// remoteAddrContextKey is the unexported context key WithRemoteAddr and
// RemoteAddrFromContext share, following the same pattern as
// tlsStateContextKey (mtls.go).
type remoteAddrContextKey struct{}

// WithRemoteAddr returns a copy of ctx carrying addr as the caller's remote
// network address, for CallerPrincipal's anonymous fallback (go-qrdb#560
// review C). Every transport that can establish one calls this where it
// first builds the request's context, BEFORE any gate that might bypass
// authentication entirely (an awaiting-admin-credential or sealed-window
// passthrough) -- those are exactly the callers CallerPrincipal would
// otherwise classify as the single shared "" anonymous principal, letting
// one source's wrong guesses lock out every other source on the same
// listener. An empty addr is not stored: it carries no more information
// than ctx already did.
func WithRemoteAddr(ctx context.Context, addr string) context.Context {
	if addr == "" {
		return ctx
	}
	return context.WithValue(ctx, remoteAddrContextKey{}, addr)
}

// RemoteAddrFromContext returns the remote address WithRemoteAddr stored in
// ctx, and whether one was present. A nil ctx is never present.
func RemoteAddrFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	addr, ok := ctx.Value(remoteAddrContextKey{}).(string)
	return addr, ok
}
