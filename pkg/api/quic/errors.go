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

// StartError is returned when the QUIC server fails to start listening.
type StartError struct {
	Addr string
	Err  error
}

// Error returns a human-readable description.
func (e *StartError) Error() string {
	return "quic: server failed to start on " + e.Addr + ": " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *StartError) Unwrap() error {
	return e.Err
}

// DispatchError is returned when a method cannot be dispatched.
type DispatchError struct {
	Method string
}

// Error returns a human-readable description.
func (e *DispatchError) Error() string {
	return "quic: unknown method: " + e.Method
}

// ServiceNotConfiguredError is returned when a handler is invoked for a
// service that was not provided during server construction. Hint, when
// set, names the remedy (e.g. "requires mode: raft").
type ServiceNotConfiguredError struct {
	Service string
	Hint    string
}

// Error returns a human-readable description.
func (e *ServiceNotConfiguredError) Error() string {
	msg := "quic: " + e.Service + " service is not configured"
	if e.Hint != "" {
		msg += ": " + e.Hint
	}
	return msg
}

// CeremonyCANotConfiguredError is returned when CeremonyInitialize is
// invoked but the server was not configured with a CeremonyCA via
// WithCeremonyCA. The ceremony service dereferences the CA to issue
// certificates, so failing closed here avoids a nil-pointer panic.
type CeremonyCANotConfiguredError struct{}

// Error returns a human-readable description.
func (e *CeremonyCANotConfiguredError) Error() string {
	return "quic: ceremony CA is not configured"
}

// InvalidArgumentError is returned when a handler receives an invalid or
// incomplete request payload.
type InvalidArgumentError struct {
	Reason string
}

// Error returns a human-readable description.
func (e *InvalidArgumentError) Error() string {
	return "quic: invalid argument: " + e.Reason
}

// MarshalError is returned when protobuf marshaling fails.
type MarshalError struct {
	Err error
}

// Error returns a human-readable description.
func (e *MarshalError) Error() string {
	return "quic: failed to marshal response: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *MarshalError) Unwrap() error {
	return e.Err
}

// UnmarshalError is returned when protobuf unmarshaling fails.
type UnmarshalError struct {
	Err error
}

// Error returns a human-readable description.
func (e *UnmarshalError) Error() string {
	return "quic: failed to unmarshal request: " + e.Err.Error()
}

// Unwrap returns the underlying error.
func (e *UnmarshalError) Unwrap() error {
	return e.Err
}

// ModuleMethodRequestTypeError is returned when a projected capability
// module's gRPC method Handler asks to decode into a value that is not a
// proto.Message. This is a construction-time bug in the module's generated
// gRPC code, not a caller error: every properly generated unary Handler
// always decodes into a *pb.XRequest, which is always a proto.Message.
type ModuleMethodRequestTypeError struct {
	Method string
}

// Error returns a human-readable description.
func (e *ModuleMethodRequestTypeError) Error() string {
	return "quic: projected method " + e.Method + ": decode target is not a proto.Message"
}

// ModuleMethodResponseTypeError is returned when a projected capability
// module's gRPC method returns a response that is not a proto.Message. Like
// ModuleMethodRequestTypeError, this reflects a bug in the module's
// generated gRPC code, not a caller error.
type ModuleMethodResponseTypeError struct {
	Method string
}

// Error returns a human-readable description.
func (e *ModuleMethodResponseTypeError) Error() string {
	return "quic: projected method " + e.Method + ": response is not a proto.Message"
}
