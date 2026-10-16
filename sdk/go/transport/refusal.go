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

package transport

import "github.com/jeremyhahn/go-qrdb/pkg/auth"

// refusalErrors rebuilds, per structured-refusal reason, the typed error a
// server refused with. Every transport carries the reason and its metadata
// beside the message -- REST's error body, gRPC's and the Unix socket's
// google.rpc.ErrorInfo detail, QUIC's error-frame info, MCP's error
// envelope -- so a client matches the error with errors.Is instead of
// parsing text, the same way an in-process caller does.
var refusalErrors = map[string]func(md map[string]string) error{
	auth.ReasonFactorConditionFailed: func(md map[string]string) error {
		return auth.FactorConditionFailedFromMetadata(md)
	},
}

// RefusalError returns the typed error a structured refusal names, or nil
// when the client does not decode reason -- a replica refusal the transport
// handles itself, or a reason from a newer server.
func RefusalError(reason string, md map[string]string) error {
	decode, ok := refusalErrors[reason]
	if !ok {
		return nil
	}
	return decode(md)
}
