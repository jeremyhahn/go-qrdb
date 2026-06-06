// Copyright 2026 Jeremy Hahn
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

package grpc

import (
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
)

// ---------------------------------------------------------------------------
// Credential error translation
//
// The eight credential RPC handlers live in service_credentials.go and
// delegate to service.CredentialServicer. They funnel every domain error
// through mapCredentialError so the gRPC status codes returned to clients
// (including the Unix/QUIC/REST/MCP transports) stay consistent across
// every transport.
// ---------------------------------------------------------------------------

// mapCredentialError converts a credentials-package error into a gRPC
// status error. Unknown errors fall through to codes.Internal.
//
// Mapping table:
//   - ErrCredentialNotFound      -> codes.NotFound
//   - ErrVersionNotFound         -> codes.NotFound
//   - ErrVersionExpired          -> codes.FailedPrecondition
//   - ErrCredentialAlreadySubmitted -> codes.AlreadyExists
//   - ErrEnumerationNotSupported -> codes.Unimplemented
//   - ErrEmptyCredentialName     -> codes.InvalidArgument
//   - ErrEmptyCredentialValue    -> codes.InvalidArgument
//   - ErrInvalidGracePeriod      -> codes.InvalidArgument
//   - ErrInvalidStrategy         -> codes.FailedPrecondition
//   - ErrNilBarrier              -> codes.FailedPrecondition
//   - default                    -> codes.Internal
func mapCredentialError(err error) error {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, credentials.ErrCredentialNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, credentials.ErrVersionNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, credentials.ErrVersionExpired):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, credentials.ErrCredentialAlreadySubmitted):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, credentials.ErrEnumerationNotSupported):
		return status.Error(codes.Unimplemented, err.Error())
	case errors.Is(err, credentials.ErrEmptyCredentialName),
		errors.Is(err, credentials.ErrEmptyCredentialValue),
		errors.Is(err, credentials.ErrInvalidGracePeriod):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, credentials.ErrInvalidStrategy),
		errors.Is(err, credentials.ErrNilBarrier):
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
