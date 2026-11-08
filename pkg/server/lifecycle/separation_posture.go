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

package lifecycle

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/wiring"
)

// RecordSeparationPosture records the separation-of-duties posture this node
// resolves from configured in the replicated system record
// wiring.SeparationPostureKey, through the node's KV service, and emits one
// audit event to sink when it differs from the recorded one or none is
// recorded. It is wiring.RecordSeparationPosture over this node's store; the
// daemon and sdk/go/node both call it once first boot has run.
func (n *Node) RecordSeparationPosture(ctx context.Context, sink audit.AuditLogger, configured bool) (bool, error) {
	return wiring.RecordSeparationPosture(ctx, newKVStoreAdapter(n.KV, nil), sink, configured)
}
