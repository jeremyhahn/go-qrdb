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

package engine

import (
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
)

// handleImportStage processes an OpImportStage command on the KV engine.
//
// Every staged record is bound to its session's lease, so that revoking or
// expiring the lease reclaims it (handleImportStage in mvcc_import_stage.go,
// pebbleHandleImportStage in pkg/store). The KV engine keeps no leases -- it
// is the file and objstore backends' engine, and lifecycle.NewLocalEngine
// hands it no lessor -- so it answers exactly as those handlers answer when
// built without a lessor: a well-formed command is refused with
// *statemachine.ServiceNotConfiguredError naming "lease", and nothing is
// written. A record staged here could never be reclaimed.
//
// A malformed payload is a decode error, as it is in those handlers.
func (e *KVEngine) handleImportStage(payload []byte) (sm.Result, error) {
	if _, err := protocol.DecodeImportStage(payload); err != nil {
		return sm.Result{}, err
	}
	return statemachine.RefusalResult(&statemachine.ServiceNotConfiguredError{Service: "lease"}), nil
}
