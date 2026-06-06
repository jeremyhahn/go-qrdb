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

package authfactor

// NilTransportError is returned by New when the inner AuthFactorClient is nil.
// Callers can match on this typed error to distinguish a programming mistake
// (forgetting to wire a transport) from a runtime failure.
type NilTransportError struct{}

// Error implements the error interface.
func (*NilTransportError) Error() string {
	return "authfactor: nil transport client"
}

// NilRequestError is returned when a Client method is called with a nil
// request struct. Method identifies which entry point received the nil.
type NilRequestError struct {
	Method string
}

// Error implements the error interface.
func (e *NilRequestError) Error() string {
	return "authfactor: nil request for " + e.Method
}
