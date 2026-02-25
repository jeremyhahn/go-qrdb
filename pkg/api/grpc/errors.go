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

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mapServiceError translates service-layer errors into gRPC status errors
// with appropriate codes. Unknown errors are mapped to codes.Internal.
func mapServiceError(err error) error {
	if err == nil {
		return nil
	}

	var knf *service.KeyNotFoundError
	if errors.As(err, &knf) {
		return status.Error(codes.NotFound, err.Error())
	}

	var nle *service.NotLeaderError
	if errors.As(err, &nle) {
		return status.Error(codes.Unavailable, err.Error())
	}

	var pe *service.ProposeError
	if errors.As(err, &pe) {
		return status.Error(codes.Internal, err.Error())
	}

	var re *service.ReadError
	if errors.As(err, &re) {
		return status.Error(codes.Internal, err.Error())
	}

	var me *service.MembershipError
	if errors.As(err, &me) {
		return status.Error(codes.Internal, err.Error())
	}

	var se *service.SnapshotRequestError
	if errors.As(err, &se) {
		return status.Error(codes.Internal, err.Error())
	}

	var de *service.DrainError
	if errors.As(err, &de) {
		return status.Error(codes.Unavailable, err.Error())
	}

	return status.Error(codes.Internal, err.Error())
}

// StartError is returned when the gRPC server fails to start listening
// on the configured address.
type StartError struct {
	Addr string
	Err  error
}

// Error returns a human-readable description.
func (e *StartError) Error() string {
	return "grpc: server failed to start on " + e.Addr + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *StartError) Unwrap() error {
	return e.Err
}
