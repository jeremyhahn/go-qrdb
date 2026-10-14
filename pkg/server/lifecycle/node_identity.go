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

package lifecycle

import (
	"github.com/jeremyhahn/go-qrdb/pkg/backend"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// LocalNodeID is the identity a standalone node names itself by in the
// barrier-key requests it makes: it has no Raft address to name itself by.
const LocalNodeID = "local"

// nodeIdentity is the name this node goes by in the barrier-key requests it
// makes: its Raft address, or LocalNodeID on a standalone node, whose
// LocalBackend has none.
func nodeIdentity(host service.Host) string {
	if host == nil || standaloneHost(host) {
		return LocalNodeID
	}
	return host.RaftAddress()
}

// standaloneHost reports whether host is a standalone node's LocalBackend.
func standaloneHost(host service.Host) bool {
	_, standalone := host.(*backend.LocalBackend)
	return standalone
}
