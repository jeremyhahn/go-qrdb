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
)

// handleWatchSetup is a placeholder handler registered in the dispatch map
// for the "watch.Watch" method. Watch requires server-streaming semantics
// that are handled by a dedicated streaming path in the QUIC server, not
// the standard request-response handleStream flow. This handler returns
// a ServiceNotConfiguredError directing callers to the streaming watch
// implementation.
func (s *Server) handleWatchSetup(_ context.Context, _ []byte) ([]byte, error) {
	return nil, &ServiceNotConfiguredError{Service: "watch"}
}

// handleWatchPrefixSetup is a placeholder handler registered in the dispatch
// map for the "watch.WatchPrefix" method. Like handleWatchSetup, prefix
// watches require server-streaming semantics and must use the dedicated
// streaming path. This handler returns a ServiceNotConfiguredError directing
// callers to the streaming watch implementation.
func (s *Server) handleWatchPrefixSetup(_ context.Context, _ []byte) ([]byte, error) {
	return nil, &ServiceNotConfiguredError{Service: "watch"}
}
