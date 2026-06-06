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

package statemachine

import (
	"errors"
	"log/slog"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
)

// resolveApplyError converts a handler error into the right action for the
// SM.Update contract. When the error is a ServiceNotConfiguredError the caller
// MUST NOT return it to Raft, because Raft treats a non-nil Update error as a
// fatal Apply failure that prevents lastApplied from advancing. This stalls
// every subsequent SyncRead on the shard until the daemon restarts.
//
// Instead, the error is encoded in sm.Result.Data so the proposer can surface
// it to its caller without poisoning consensus. All other errors are returned
// verbatim so genuine infrastructure failures still halt Apply.
//
// Returns (result, stop). When stop is true the outer loop should return the
// (possibly nil) error to Raft immediately. When stop is false the outer loop
// should assign the returned result to results[i] and continue.
func resolveApplyError(log *slog.Logger, entryIndex uint64, handlerResult sm.Result, handlerErr error) (sm.Result, bool, error) {
	if handlerErr == nil {
		return handlerResult, false, nil
	}
	var sncErr *ServiceNotConfiguredError
	if errors.As(handlerErr, &sncErr) {
		if log != nil {
			log.Error("statemachine: subsystem not configured, encoding as result",
				slog.String("service", sncErr.Service),
				slog.Uint64("entry_index", entryIndex))
		}
		return sm.Result{Data: []byte(handlerErr.Error())}, false, nil
	}
	return sm.Result{}, true, handlerErr
}
