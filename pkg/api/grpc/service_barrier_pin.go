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

package grpc

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/pin"
)

// pinErrorCodes maps PIN sentinel errors to gRPC status codes for the
// barrier handlers' SO PIN / User PIN gate. Unknown PIN errors fall
// through to codes.Internal via mapPINError.
var pinErrorCodes = map[error]codes.Code{
	pin.ErrPINNotSet:         codes.FailedPrecondition,
	pin.ErrPINLocked:         codes.ResourceExhausted,
	pin.ErrPINInvalid:        codes.Unauthenticated,
	pin.ErrSOPINRequired:     codes.Unauthenticated,
	pin.ErrPINTooShort:       codes.InvalidArgument,
	pin.ErrPINAlreadySet:     codes.AlreadyExists,
	pin.ErrInvalidCurrentPIN: codes.Unauthenticated,
	pin.ErrStateCorrupted:    codes.DataLoss,
	pin.ErrStrategyNotSet:    codes.FailedPrecondition,
}

// mapPINError converts a PIN error from pin.PINBackend into a gRPC status
// error using pinErrorCodes. Unknown errors map to codes.Internal.
func mapPINError(err error) error {
	if err == nil {
		return nil
	}
	for sentinel, code := range pinErrorCodes {
		if errors.Is(err, sentinel) {
			return status.Error(code, err.Error())
		}
	}
	return status.Error(codes.Internal, err.Error())
}

// applyBarrierInitializePINs forwards SO PIN / User PIN supplied on the
// initialize request to the configured pin.PINBackend. When no backend
// is configured the PIN fields are silently ignored to preserve dev-mode
// flows.
func applyBarrierInitializePINs(backend pin.PINBackend, soPIN, userPIN string) error {
	if backend == nil {
		return nil
	}
	if soPIN != "" {
		if err := backend.SetSOPIN("", soPIN); err != nil {
			return err
		}
	}
	if userPIN != "" {
		if err := backend.SetUserPIN(soPIN, userPIN); err != nil {
			return err
		}
	}
	return nil
}

// verifyBarrierSOPIN gates an administrative barrier operation behind SO
// PIN authorization. Enforcement only kicks in when the backend is
// configured AND has a SO PIN set.
func verifyBarrierSOPIN(backend pin.PINBackend, soPIN string) error {
	if backend == nil {
		return nil
	}
	if !backend.SOPINSet() {
		return nil
	}
	return backend.VerifySOPIN(soPIN)
}
