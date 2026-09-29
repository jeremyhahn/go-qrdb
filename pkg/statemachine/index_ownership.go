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

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
)

// indexManagerFailure turns an IndexManager create or drop error into the
// state machine's answer.
//
// An ownership refusal (go-qrdb#426) is a deterministic consequence of
// committed input, so it is encoded into the RESULT as an application error
// and the apply advances; every replica reaches the same refusal. Any other
// error is the manager failing and stays a StorageError, as it was.
func indexManagerFailure(op string, err error) (sm.Result, error) {
	var own *index.OwnershipError
	if errors.As(err, &own) {
		return sm.Result{Data: protocol.EncodeIndexOwnershipError(own.Name, own.Owner)}, nil
	}
	return sm.Result{}, &StorageError{Op: op, Inner: err}
}
