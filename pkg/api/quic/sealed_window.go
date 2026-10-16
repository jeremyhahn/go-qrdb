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

package quic

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
)

// SealedWindowAllowlist is the set of native QUIC method names that
// remain operative while the barrier is sealed. Methods not in this
// list -- admin.Health and admin.Status included -- are refused with
// wire.StatusUnavailable and reason sealedwindow.ReasonSealed while
// s.sealedWindowProbe reports sealed. The list is derived from the canonical
// transport-neutral verb table in pkg/api/sealedwindow so it cannot drift
// from the other transports: the unseal call and the first-boot verbs.
var SealedWindowAllowlist = sealedwindow.QUICAllowlist()

// sealedWindowAllowed reports whether method remains reachable while the
// barrier is sealed.
func sealedWindowAllowed(method string) bool {
	_, ok := SealedWindowAllowlist[method]
	return ok
}

// sealedWindowTransition holds the native QUIC methods of the Transition
// verbs (initialize and unseal), which run alone on the boot-time listener.
var sealedWindowTransition = sealedwindow.QUICTransition()

// sealedWindowPreOperationalOnly holds the native QUIC methods of the
// PreOperationalOnly verbs: the first-boot verbs a sealed node serves only
// while enrollment is open.
var sealedWindowPreOperationalOnly = sealedwindow.QUICPreOperationalOnly()

// sealedWindowAuthenticatedOnly holds the native QUIC methods of the
// AuthenticatedOnly verbs (cluster unseal), never served anonymously.
var sealedWindowAuthenticatedOnly = sealedwindow.QUICAuthenticatedOnly()

// sealedWindowListenerOnly holds the native QUIC methods of the ListenerOnly
// verbs (barrier status), never served by a sealed regular listener.
var sealedWindowListenerOnly = sealedwindow.QUICListenerOnly()

// sealedWindowRefusal is how the QUIC gate answers one refusing Decision.
type sealedWindowRefusal struct {
	status uint16
	// payload is the error frame's body. The sealed refusal's info carries
	// sealedwindow.ReasonSealed so a client reads "up and sealed" from it,
	// and the unconfirmed refusal's carries SEAL_STATE_UNCONFIRMED.
	payload wire.ErrorPayload
}

// sealedWindowRefusals maps each refusing sealedwindow.Decision to its
// response. sealedwindow.Serve is absent: it admits.
var sealedWindowRefusals = map[sealedwindow.Decision]sealedWindowRefusal{
	sealedwindow.RefuseSealed: {
		status: wire.StatusUnavailable,
		payload: wire.ErrorPayload{
			Message: sealedwindow.SealedMessage,
			Info:    &wire.ErrorInfo{Domain: sealedwindow.ErrorDomain, Reason: sealedwindow.ReasonSealed},
		},
	},
	sealedwindow.RefuseUnconfirmed: {status: wire.StatusUnavailable, payload: errorPayloadOf(&clusterseal.SealStateUnconfirmedError{})},
	sealedwindow.RefuseClosed:      {status: wire.StatusUnavailable, payload: wire.ErrorPayload{Message: sealedwindow.ClosedMessage}},
}

// sealedWindowGateRefusal applies the sealed-window gate of probe to method:
// a regular listener's when listener is nil, and the boot-time sealed-window
// listener's otherwise. It reports the refusal and true when the gate refuses
// the request, and false when it admits it or probe is nil.
func sealedWindowGateRefusal(ctx context.Context, method string, probe sealedWindowProbe, listener sealedwindow.ListenerProbe) (sealedWindowRefusal, bool) {
	if probe == nil {
		return sealedWindowRefusal{}, false
	}
	_, authOnly := sealedWindowAuthenticatedOnly[method]
	_, listenerOnly := sealedWindowListenerOnly[method]
	sealed, unconfirmed := sealedwindow.Sample(probe)
	req := sealedwindow.Request{
		Sealed:            sealed,
		Unconfirmed:       unconfirmed,
		Allowlisted:       sealedWindowAllowed(method),
		Listener:          listener != nil,
		AuthenticatedOnly: authOnly,
		ListenerOnly:      listenerOnly,
	}
	if _, preOp := sealedWindowPreOperationalOnly[method]; preOp && req.Sealed && req.Allowlisted {
		req.PreOperationalOnly = true
		req.EnrollmentOpen = listener != nil && listener.EnrollmentOpen(ctx)
	}
	refusal, refused := sealedWindowRefusals[sealedwindow.Decide(req)]
	return refusal, refused
}
