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

package main

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/wiring"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// recordSeparationPostureAtStart records the separation-of-duties posture the
// daemon resolves from configured in the replicated system record
// wiring.SeparationPostureKey, through kvSvc, the codec-free store the user
// and role records use, and emits one posture-change event to sink when the
// recorded posture differs or none is recorded (#1128). A record that cannot
// be read or written answers *wiring.SeparationPostureError, which refuses
// the start.
func recordSeparationPostureAtStart(ctx context.Context, kvSvc service.KVServicer, sink audit.AuditLogger, configured bool) error {
	_, err := wiring.RecordSeparationPosture(ctx, newKVServiceAdapter(kvSvc, nil, nil), sink, configured)
	return err
}
