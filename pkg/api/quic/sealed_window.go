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

import "github.com/jeremyhahn/go-qrdb/pkg/api/sealedwindow"

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
