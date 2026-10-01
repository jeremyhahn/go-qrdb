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
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"
)

// SealedWindowAllowlist is the set of native QUIC method names that
// remain operative while the barrier is sealed. Methods not in this
// list are rejected with wire.StatusUnavailable while s.sealedWindowProbe
// reports sealed. The list is derived from the canonical
// transport-neutral verb table in pkg/api/sealedwindow so it cannot
// drift from the other transports: every barrier.* and ceremony.*
// unseal-flow method, plus admin.Health/admin.Status for liveness
// probes.
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
// PreOperationalOnly verbs (officer and recovery-officer registration).
var sealedWindowPreOperationalOnly = sealedwindow.QUICPreOperationalOnly()

// sealedWindowRefusal is how the QUIC gate answers one refusing Decision.
type sealedWindowRefusal struct {
	status uint16
	msg    string
}

// sealedWindowRefusals maps each refusing sealedwindow.Decision to its
// response. sealedwindow.Serve is absent: it admits.
var sealedWindowRefusals = map[sealedwindow.Decision]sealedWindowRefusal{
	sealedwindow.RefuseSealed:           {status: wire.StatusUnavailable, msg: "barrier sealed"},
	sealedwindow.RefuseClosed:           {status: wire.StatusUnavailable, msg: sealedwindow.ClosedMessage},
	sealedwindow.RefuseEnrollmentClosed: {status: wire.StatusPermissionDenied, msg: sealedwindow.EnrollmentClosedMessage},
}

// sealedWindowRefusal applies the sealed-window gate to method. It reports
// the refusal and true when the gate refuses the request, and false when it
// admits it or no gate is installed.
func (s *Server) sealedWindowRefusal(method string) (sealedWindowRefusal, bool) {
	if s.sealedWindowProbe == nil {
		return sealedWindowRefusal{}, false
	}
	req := sealedwindow.Request{
		Sealed:      s.sealedWindowProbe.IsSealed(),
		Allowlisted: sealedWindowAllowed(method),
		Listener:    s.sealedWindowListener != nil,
	}
	if _, preOp := sealedWindowPreOperationalOnly[method]; preOp && req.Listener && req.Sealed && req.Allowlisted {
		req.PreOperationalOnly = true
		req.EnrollmentOpen = s.sealedWindowListener.EnrollmentOpen()
	}
	refusal, refused := sealedWindowRefusals[sealedwindow.Decide(req)]
	return refusal, refused
}
