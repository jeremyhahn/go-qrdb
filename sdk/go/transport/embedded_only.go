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

package transport

import (
	"errors"
	"fmt"
)

// ErrEmbeddedOnly is the sentinel every remote transport returns for an
// operation in EmbeddedOnlyOperations.
var ErrEmbeddedOnly = errors.New("transport: operation is embedded-only; no remote transport serves it")

// ReasonEmbeddedOnly is the structured-refusal reason for an
// *EmbeddedOnlyError; metadata keys "operation" and "transport".
const ReasonEmbeddedOnly = "EMBEDDED_ONLY"

// Seal-store operation names: the SealStoreClient methods.
const (
	OpSealStorePut     = "SealStorePut"
	OpSealStoreGet     = "SealStoreGet"
	OpSealStoreDelete  = "SealStoreDelete"
	OpSealStoreList    = "SealStoreList"
	OpSealStoreReseal  = "SealStoreReseal"
	OpSealStoreStatus  = "SealStoreStatus"
	embeddedOnlyMDOp   = "operation"
	embeddedOnlyMDName = "transport"
)

// EmbeddedOnlyOperations is the one documented list of operations no remote
// transport serves. Owner ruling 1234 (2026-11-23): "the seal store DATA is
// never reachable over the network, but seal and unseal ARE, or the system
// could not be unsealed. So: SealStoreClient data and management verbs are a
// named embedded-only exclusion that remote transports refuse; the seal and
// unseal operations stay on the transports."
//
// Every remote transport (gRPC, gRPC-Web, unix, REST, QUIC, MCP) answers each
// listed operation with RefuseEmbeddedOnly and no network I/O; the server
// guard tests consult this list so no listed name is ever registered as a
// route, RPC, method or tool. Unix counts as remote: embedded-only means
// in-process only.
var EmbeddedOnlyOperations = []string{
	OpSealStorePut,
	OpSealStoreGet,
	OpSealStoreDelete,
	OpSealStoreList,
	OpSealStoreReseal,
	OpSealStoreStatus,
}

// IsEmbeddedOnly reports whether op is in EmbeddedOnlyOperations.
func IsEmbeddedOnly(op string) bool {
	for _, name := range EmbeddedOnlyOperations {
		if name == op {
			return true
		}
	}
	return false
}

// EmbeddedOnlyError is the typed refusal of an embedded-only operation on a
// remote transport. It unwraps to ErrEmbeddedOnly.
type EmbeddedOnlyError struct {
	// Operation is the refused operation, one of EmbeddedOnlyOperations.
	Operation string
	// Transport names the remote transport that refused it.
	Transport string
}

// Error names the operation and the transport.
func (e *EmbeddedOnlyError) Error() string {
	return fmt.Sprintf("transport %s: %s is embedded-only (ruling 1234); use the embedded transport: %v",
		e.Transport, e.Operation, ErrEmbeddedOnly)
}

// Unwrap returns ErrEmbeddedOnly.
func (e *EmbeddedOnlyError) Unwrap() error { return ErrEmbeddedOnly }

// Reason returns ReasonEmbeddedOnly.
func (e *EmbeddedOnlyError) Reason() string { return ReasonEmbeddedOnly }

// Metadata returns the structured-refusal metadata RefusalError rebuilds
// the error from.
func (e *EmbeddedOnlyError) Metadata() map[string]string {
	return map[string]string{embeddedOnlyMDOp: e.Operation, embeddedOnlyMDName: e.Transport}
}

// EmbeddedOnlyFromMetadata rebuilds an *EmbeddedOnlyError from refusal
// metadata.
func EmbeddedOnlyFromMetadata(md map[string]string) error {
	return &EmbeddedOnlyError{Operation: md[embeddedOnlyMDOp], Transport: md[embeddedOnlyMDName]}
}

// RefuseEmbeddedOnly returns the typed refusal a remote transport answers an
// embedded-only operation with.
func RefuseEmbeddedOnly(transportName, op string) error {
	return &EmbeddedOnlyError{Operation: op, Transport: transportName}
}
