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

package release

import "strconv"

// GapError refuses a peer whose release is outside the range this node
// accepts (see Adjacent). It names the channel the peer arrived on and both
// releases, so the operator reading either node's log sees the gap.
type GapError struct {
	// Channel is the node-to-node channel that refused the peer, for
	// example "enrollment" or "raft-membership".
	Channel string
	// Local is the refusing node's release.
	Local string
	// Peer is the release the peer announced; empty when it announced none.
	Peer string
}

// Error names the channel and both versions.
func (e *GapError) Error() string {
	return "release: " + e.Channel + ": peer release " + strconv.Quote(e.Peer) +
		" is not adjacent to local release " + strconv.Quote(e.Local) +
		"; a cluster runs only adjacent releases (same or consecutive minor)"
}
