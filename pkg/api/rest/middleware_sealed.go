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

package rest

import (
	"net/http"

	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
)

// sealedWindowProbe is the abstraction the middleware needs to know
// whether the storage barrier is currently sealed. Implemented by
// *seal.StorageBarrier in production; defined as an interface so
// tests can substitute a deterministic fake without depending on the
// full barrier construction machinery.
type sealedWindowProbe interface {
	IsSealed() bool
}

// SealedWindowAllowlist is the set of REST entries that remain operative
// while the barrier is sealed. Each entry is a path or a method-qualified
// "METHOD /path"; a path ending in "/" is matched as a prefix (see
// sealedwindow.RESTEntryMatches). The list is derived from the canonical
// transport-neutral verb table in pkg/api/sealedwindow so it cannot drift
// from the other transports; it remains exposed as a package-level var so
// callers can compose with project-specific extensions if they ever need to.
var SealedWindowAllowlist = sealedwindow.RESTAllowlist()

// sealedResponseBody is the JSON envelope returned to clients while
// the barrier is sealed and they hit a non-allowlist endpoint.
const sealedResponseBody = `{"error":"barrier sealed","code":"SEALED"}`

// closedResponseBody is the JSON envelope the boot-time sealed-window
// listener returns for every request once the barrier is unsealed.
const closedResponseBody = `{"error":"` + sealedwindow.ClosedMessage + `","code":"SEALED_WINDOW_CLOSED"}`

// enrollmentClosedResponseBody is the JSON envelope the boot-time
// sealed-window listener returns for an anonymous officer registration once
// enrollment has closed.
const enrollmentClosedResponseBody = `{"error":"` + sealedwindow.EnrollmentClosedMessage + `","code":"ENROLLMENT_CLOSED"}`

// sealedWindowTransition holds the REST entries of the Transition verbs
// (initialize and unseal), which run alone on the boot-time listener.
var sealedWindowTransition = sealedwindow.RESTTransition()

// sealedWindowPreOperationalOnly holds the REST entries of the
// PreOperationalOnly verbs (officer and recovery-officer registration).
var sealedWindowPreOperationalOnly = sealedwindow.RESTPreOperationalOnly()

// sealedWindowRefusal is how the REST gate answers one refusing Decision.
type sealedWindowRefusal struct {
	status     int
	body       string
	retryAfter bool
}

// sealedWindowRefusals maps each refusing sealedwindow.Decision to its
// response. sealedwindow.Serve is absent: it admits.
var sealedWindowRefusals = map[sealedwindow.Decision]sealedWindowRefusal{
	sealedwindow.RefuseSealed:           {status: http.StatusServiceUnavailable, body: sealedResponseBody, retryAfter: true},
	sealedwindow.RefuseClosed:           {status: http.StatusServiceUnavailable, body: closedResponseBody, retryAfter: true},
	sealedwindow.RefuseEnrollmentClosed: {status: http.StatusForbidden, body: enrollmentClosedResponseBody},
}

// SealedWindowMiddleware returns an http middleware that 503s every
// request not on the allowlist while the supplied barrier is
// sealed. Once the barrier is unsealed the middleware becomes a
// passthrough; on re-seal it engages again. The same middleware
// instance is safe to keep installed across the lifetime of the
// process. It is the gate of a regular listener, whose own
// authentication middleware decides every request it passes.
//
// allowlist may be nil, in which case SealedWindowAllowlist is used.
//
// The middleware sets a Retry-After: 5 header on rejected responses
// so well-behaved clients back off during the unseal window.
func SealedWindowMiddleware(barrier sealedWindowProbe, allowlist []string) func(http.Handler) http.Handler {
	return sealedWindowGate(barrier, allowlist, nil)
}

// SealedWindowListenerMiddleware returns the gate of the boot-time
// sealed-window listener. While the barrier is sealed it serves only the
// allowlist, exactly as SealedWindowMiddleware does. Once the barrier is
// unsealed it refuses EVERY request with 503: that listener runs without
// authentication, so it must not serve anything against an unsealed
// barrier, even in the moment before it is stopped. While sealed it also
// refuses officer and recovery-officer registration with 403
// ENROLLMENT_CLOSED once probe.EnrollmentOpen reports enrollment closed. The
// probe is sampled on every request; a nil probe refuses every request.
func SealedWindowListenerMiddleware(probe sealedwindow.ListenerProbe, allowlist []string) func(http.Handler) http.Handler {
	probe = sealedwindow.OrFailClosed(probe)
	return sealedWindowGate(probe, allowlist, probe)
}

// sealedWindowGate applies sealedwindow.Decide to every request. listener is
// nil for a regular listener's gate and the probe of the boot-time
// sealed-window listener otherwise.
func sealedWindowGate(barrier sealedWindowProbe, allowlist []string, listener sealedwindow.ListenerProbe) func(http.Handler) http.Handler {
	if allowlist == nil {
		allowlist = SealedWindowAllowlist
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if listener != nil {
				// Held until the request completes; see sealedwindow.Admission.
				defer listener.Admit(requestInAllowlist(r, sealedWindowTransition))()
			}
			req := sealedwindow.Request{
				Sealed:      barrier != nil && barrier.IsSealed(),
				Allowlisted: requestInAllowlist(r, allowlist),
				Listener:    listener != nil,
			}
			if req.Listener && req.Sealed && req.Allowlisted && requestInAllowlist(r, sealedWindowPreOperationalOnly) {
				req.PreOperationalOnly = true
				req.EnrollmentOpen = listener.EnrollmentOpen()
			}
			refusal, refused := sealedWindowRefusals[sealedwindow.Decide(req)]
			if !refused {
				if req.Listener {
					r = r.WithContext(sealedwindow.WithAnonymous(r.Context()))
				}
				next.ServeHTTP(w, r)
				return
			}
			writeSealedWindowRefusal(w, refusal)
		})
	}
}

// writeSealedWindowRefusal writes a sealed-window refusal.
func writeSealedWindowRefusal(w http.ResponseWriter, refusal sealedWindowRefusal) {
	w.Header().Set("Content-Type", "application/json")
	if refusal.retryAfter {
		w.Header().Set("Retry-After", "5")
	}
	w.WriteHeader(refusal.status)
	_, _ = w.Write([]byte(refusal.body))
}

// requestInAllowlist reports whether r matches an entry in the allowlist.
func requestInAllowlist(r *http.Request, allowlist []string) bool {
	for _, entry := range allowlist {
		if sealedwindow.RESTEntryMatches(entry, r.Method, r.URL.Path) {
			return true
		}
	}
	return false
}
