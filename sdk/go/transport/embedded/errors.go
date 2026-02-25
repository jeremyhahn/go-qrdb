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

package embedded

import (
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// NilServiceError is returned when a nil service is passed to New.
type NilServiceError struct {
	Service string
}

// Error returns a human-readable description.
func (e *NilServiceError) Error() string {
	return "embedded: " + e.Service + " must not be nil"
}

// UnknownMethodError is returned when Request is called with an
// unrecognized method name.
type UnknownMethodError struct {
	Method string
}

// Error returns a human-readable description.
func (e *UnknownMethodError) Error() string {
	return "embedded: unknown method: " + e.Method
}

// InvalidRequestError is returned when the request type does not match
// the expected type for a given method.
type InvalidRequestError struct {
	Method string
}

// Error returns a human-readable description.
func (e *InvalidRequestError) Error() string {
	return "embedded: invalid request type for method: " + e.Method
}

// translateError converts service-layer errors to transport-layer errors
// so callers can use errors.As with transport.KeyNotFoundError,
// transport.NotLeaderError, etc.
func translateError(err error) error {
	if err == nil {
		return nil
	}

	var knf *service.KeyNotFoundError
	if errors.As(err, &knf) {
		return &transport.KeyNotFoundError{Key: knf.Key}
	}

	var nle *service.NotLeaderError
	if errors.As(err, &nle) {
		return &transport.NotLeaderError{
			ShardID:       nle.ShardID,
			LeaderID:      nle.LeaderID,
			LeaderAddress: nle.LeaderAddress,
		}
	}

	return &transport.RequestError{Method: "embedded", Err: err}
}
