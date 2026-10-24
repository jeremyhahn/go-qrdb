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
// "METHOD /path"; a path ending in "/" is matched as a prefix and a path
// with a "{name}" segment as a one-segment pattern (see
// sealedwindow.RESTEntryMatches). The list is derived from the canonical
// transport-neutral verb table in pkg/api/sealedwindow so it cannot drift
// from the other transports; it remains exposed as a package-level var so
// callers can compose with project-specific extensions if they ever need to.
var SealedWindowAllowlist = sealedwindow.RESTAllowlist()

// sealedResponseBody is the JSON envelope returned to clients while the
// barrier is sealed and they call anything but the unseal call. Its code is
// the sealed refusal's structured reason, in the field a replica refusal
// carries its own (errorResponse.Code).
const sealedResponseBody = `{"error":"` + sealedwindow.SealedMessage + `","code":"` + sealedwindow.ReasonSealed + `"}`

// unconfirmedResponseBody is the envelope returned while the barrier is
// unsealed but this node cannot confirm the cluster seal state -- its lease on
// the seal records ran out. It is retryable like any 503, and it is not SEALED:
// a client told SEALED sends an operator to unseal a barrier that is open.
const unconfirmedResponseBody = `{"error":"clusterseal: this node cannot confirm the cluster seal state; retry","code":"SEAL_STATE_UNCONFIRMED"}`

// closedResponseBody is the JSON envelope the boot-time sealed-window
// listener returns for every request once the barrier is unsealed.
const closedResponseBody = `{"error":"` + sealedwindow.ClosedMessage + `","code":"SEALED_WINDOW_CLOSED"}`

// sealedWindowTransition holds the REST entries of the Transition verbs
// (initialize and unseal), which run alone on the boot-time listener.
var sealedWindowTransition = sealedwindow.RESTTransition()

// sealedWindowPreOperationalOnly holds the REST entries of the
// PreOperationalOnly verbs: the first-boot verbs a sealed node serves only
// while enrollment is open.
var sealedWindowPreOperationalOnly = sealedwindow.RESTPreOperationalOnly()

// sealedWindowAuthenticatedOnly holds the REST entries of the
// AuthenticatedOnly verbs (cluster unseal), never served anonymously.
var sealedWindowAuthenticatedOnly = sealedwindow.RESTAuthenticatedOnly()

// sealedWindowListenerOnly holds the REST entries of the ListenerOnly verbs
// (barrier status), never served by a sealed regular listener.
var sealedWindowListenerOnly = sealedwindow.RESTListenerOnly()

// sealedWindowRefusal is how the REST gate answers one refusing Decision.
type sealedWindowRefusal struct {
	status     int
	body       string
	retryAfter bool
}

// sealedWindowRefusals maps each refusing sealedwindow.Decision to its
// response. sealedwindow.Serve is absent: it admits.
var sealedWindowRefusals = map[sealedwindow.Decision]sealedWindowRefusal{
	sealedwindow.RefuseSealed:      {status: http.StatusServiceUnavailable, body: sealedResponseBody, retryAfter: true},
	sealedwindow.RefuseUnconfirmed: {status: http.StatusServiceUnavailable, body: unconfirmedResponseBody, retryAfter: true},
	sealedwindow.RefuseClosed:      {status: http.StatusServiceUnavailable, body: closedResponseBody, retryAfter: true},
}

// SealedWindowMiddleware returns an http middleware that 503s every
// request not on the allowlist while the supplied barrier is
// sealed. It has no enrollment probe, so it refuses the first-boot verbs
// too: in qrdb it sees only a cluster-held node, whose barrier is unsealed
// and initialized, because a bare sealed barrier is dispatched to the
// sealed-window chain (sealedWindowDispatch). Once the barrier is unsealed the middleware becomes a
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
// barrier, even in the moment before it is stopped. While sealed it serves
// the first-boot verbs only while probe.EnrollmentOpen reports enrollment
// open, and refuses them as sealed otherwise. The probe is sampled on every
// request; a nil probe refuses every request.
func SealedWindowListenerMiddleware(probe sealedwindow.ListenerProbe, allowlist []string) func(http.Handler) http.Handler {
	probe = sealedwindow.OrFailClosed(probe)
	return sealedWindowGate(probe, allowlist, probe)
}

// sealedWindowDispatch returns the middleware a re-sealed node's regular
// listener shares with the boot-time sealed-window listener: a request that
// arrives while probe reports the barrier sealed is served by router behind
// SealedWindowListenerMiddleware, and every other request by the wrapped
// regular chain. That gate samples probe again, so a request dispatched just
// before an unseal is refused rather than served anonymously against the
// unsealed barrier.
func sealedWindowDispatch(probe sealedwindow.ListenerProbe, router http.Handler) func(http.Handler) http.Handler {
	sealed := SealedWindowListenerMiddleware(probe, nil)(router)
	return func(regular http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if probe.IsSealed() {
				sealed.ServeHTTP(w, r)
				return
			}
			regular.ServeHTTP(w, r)
		})
	}
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
			transition := requestInAllowlist(r, sealedWindowTransition)
			if listener != nil {
				// Held until the request completes; see sealedwindow.Admission.
				defer listener.Admit(transition)()
			}
			sealed, unconfirmed := sealedwindow.Sample(barrier)
			req := sealedwindow.Request{
				Sealed:            sealed,
				Unconfirmed:       unconfirmed,
				Allowlisted:       requestInAllowlist(r, allowlist),
				Listener:          listener != nil,
				AuthenticatedOnly: requestInAllowlist(r, sealedWindowAuthenticatedOnly),
				ListenerOnly:      requestInAllowlist(r, sealedWindowListenerOnly),
			}
			if req.Sealed && req.Allowlisted && requestInAllowlist(r, sealedWindowPreOperationalOnly) {
				req.PreOperationalOnly = true
				req.EnrollmentOpen = listener != nil && listener.EnrollmentOpen(r.Context())
			}
			decision := sealedwindow.Decide(req)
			if req.Listener && r.ProtoMajor == 1 && (transition || decision == sealedwindow.RefuseClosed) {
				// The listener is torn down once the barrier leaves the sealed
				// state, and http.Server.Shutdown closes its idle keep-alive
				// connections: a client's next request written onto one meets
				// an EOF it cannot tell from a lost response. The close option
				// (RFC 9112 §9.6) ends the connection with this response, so
				// that request opens a new one and is either answered here or
				// refused at connect. HTTP/2 and HTTP/3 forbid the field
				// (RFC 9113 §8.2.2, RFC 9114 §4.2) and end a connection with
				// GOAWAY instead.
				w.Header().Set("Connection", "close")
			}
			refusal, refused := sealedWindowRefusals[decision]
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
