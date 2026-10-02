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
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
)

// moduleStatus translates a gRPC status error raised by a PROJECTED
// capability module into a wire status.
//
// registerModules dispatches a module's unary grpc.ServiceDesc methods as QUIC
// frames (see modules.go), so a module's handler answers in gRPC's vocabulary
// by construction — it has never seen this wire and cannot be asked to. Before
// the vocabulary existed every one of those answers arrived here unclassified
// and left as StatusError, so a module's NotFound and its Internal were the
// same frame.
//
// The second return reports whether err carried a gRPC status at all. It is
// false for every ordinary Go error, including all of qrdb's own typed service
// errors, which is what keeps this clause from shadowing the classification
// mapServiceError does for them.
func moduleStatus(err error) (uint16, bool) {
	st, ok := status.FromError(err)
	if !ok {
		// status.FromError reports false for an error that carries no gRPC
		// status, and hands back a codes.Unknown status it synthesised. Using
		// that would map every unclassified error in the process to
		// StatusUnknown.
		return 0, false
	}
	return wire.StatusForGRPCCode(st.Code())
}
