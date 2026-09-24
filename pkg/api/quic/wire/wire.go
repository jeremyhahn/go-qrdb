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

// Package wire provides the binary framing protocol for native QUIC
// transport. It defines the request/response wire format, status codes,
// and read/write functions shared by both server and SDK client.
//
// Wire format:
//
//	Request:  [methodLen:uint16][method:string][dbLen:uint16][database:string][requestID:uint64][payloadLen:uint32][payload:protobuf]
//	Response: [requestID:uint64][statusCode:uint16][payloadLen:uint32][payload:protobuf]
//
// The database field carries the target database scope within the
// authenticated tenant's keyspace (the QUIC peer of the REST X-QRDB-Database
// header and the gRPC x-qrdb-database metadata). An empty database selects the
// implicit bootstrap database on the server.
package wire

import (
	"encoding/binary"
	"io"
	"strconv"
)

// Status codes returned in QUIC response frames.
//
// # The vocabulary is gRPC's seventeen conditions, under this protocol's own numbering
//
// The set of CONDITIONS this wire can report is exactly the set gRPC's status
// codes name — one wire status per gRPC code, seventeen of each, no condition
// in either that the other cannot express. That correspondence is written down
// once, in pkg/api/quic.wireStatusByGRPCCode, and asserted to be a bijection by
// TestWireStatusByGRPCCode_IsABijection.
//
// It was chosen rather than a smaller set for three measured reasons:
//
//  1. The server already speaks this vocabulary. pkg/api/grpc emits fifteen of
//     the seventeen codes across roughly eighty classified conditions; QUIC
//     declared six, so every InvalidArgument, AlreadyExists, NotFound,
//     FailedPrecondition, Aborted, OutOfRange, Canceled, ResourceExhausted and
//     DataLoss the service layer produced arrived on this wire as the generic
//     StatusError. "QUIC is deliberately narrower" described the frame layout,
//     never the conditions the handlers behind it can reach.
//  2. This wire PROJECTS gRPC services onto its frames. A capability module
//     registers a grpc.ServiceDesc and pkg/api/quic/modules.go dispatches its
//     unary methods as QUIC frames, so a module's handler answers in gRPC's
//     vocabulary by construction. Anything narrower than a 1:1 correspondence
//     is lossy for every projected module, permanently.
//  3. Four separate pieces of work escalated to the missing vocabulary rather
//     than inventing a status locally. Deciding the whole set once is what
//     stops the set from being whatever the last four issues happened to need.
//
// The cost, stated plainly: two of the seventeen (StatusDeadlineExceeded,
// StatusUnknown) are never produced by qrdb's own handlers today and exist
// only so the projection in (2) is total. A partial map there would silently
// degrade a module's error to StatusError, which is the defect this whole
// vocabulary exists to remove.
//
// # Numbering extends, it never renumbers
//
// Codes 0-5 keep the values they have always had: a renumbering is permitted
// pre-1.0 but breaks every deployed peer simultaneously and buys nothing. The
// consequence is that the numbering is qrdb's own and does NOT equal gRPC's —
// StatusNotFound is 6 here and codes.NotFound is 5 — so the correspondence is
// by MEANING and goes through the map named above, never through a cast.
//
// A future condition appends at the next free number. It is a one-constant
// change here plus one row in that map, and the bijection test names the row
// that is missing.
//
// # Version skew
//
// A client built before a status existed reads it as an unrecognised number
// and falls through to its generic request error — degraded to exactly the
// behaviour it had before the status was added, never a misclassification.
// Server and client are shipped from this repository together; a mixed
// deployment therefore loses precision on old clients and nothing else.
const (
	// StatusOK reports success. Peer of codes.OK.
	StatusOK uint16 = 0

	// StatusError reports a server-side fault the server could not classify.
	// Peer of codes.Internal: it is this protocol's fallback exactly as
	// Internal is gRPC's. It predates the vocabulary and keeps its name.
	StatusError uint16 = 1

	// StatusUnimplemented reports a healthy node that does not carry the verb.
	// Peer of codes.Unimplemented.
	StatusUnimplemented uint16 = 2

	// StatusUnauthenticated reports a caller that did not authenticate.
	// Peer of codes.Unauthenticated.
	StatusUnauthenticated uint16 = 3

	// StatusPermissionDenied reports an authenticated caller that may not.
	// Peer of codes.PermissionDenied.
	StatusPermissionDenied uint16 = 4

	// StatusUnavailable reports a retryable node-side condition — a sealed
	// barrier, a closed store, a non-leader with no resolver, a drain.
	// Peer of codes.Unavailable.
	StatusUnavailable uint16 = 5

	// StatusNotFound reports that the addressed subject does not exist.
	// Peer of codes.NotFound.
	StatusNotFound uint16 = 6

	// StatusInvalidArgument reports a request the server refused on its own
	// merits. Peer of codes.InvalidArgument.
	StatusInvalidArgument uint16 = 7

	// StatusAlreadyExists reports a create that collided with a live subject.
	// Peer of codes.AlreadyExists.
	StatusAlreadyExists uint16 = 8

	// StatusFailedPrecondition reports a request valid in itself that the
	// system's current state refuses. Peer of codes.FailedPrecondition.
	StatusFailedPrecondition uint16 = 9

	// StatusResourceExhausted reports a quota, a rate limit or a bound on
	// request size. Peer of codes.ResourceExhausted.
	StatusResourceExhausted uint16 = 10

	// StatusAborted reports a concurrency conflict the caller may retry from
	// the top. Peer of codes.Aborted.
	StatusAborted uint16 = 11

	// StatusOutOfRange reports an address outside the range the server still
	// holds. Peer of codes.OutOfRange.
	StatusOutOfRange uint16 = 12

	// StatusCanceled reports work the caller or the server stopped.
	// Peer of codes.Canceled.
	StatusCanceled uint16 = 13

	// StatusDataLoss reports unrecoverable corruption of persisted state.
	// Peer of codes.DataLoss.
	StatusDataLoss uint16 = 14

	// StatusDeadlineExceeded reports a deadline that elapsed before the work
	// completed. Peer of codes.DeadlineExceeded. Reachable only through a
	// projected capability module; qrdb's own handlers never produce it.
	StatusDeadlineExceeded uint16 = 15

	// StatusUnknown reports an error the ORIGIN could not classify — distinct
	// from StatusError, which is this server declining to classify. Peer of
	// codes.Unknown. Reachable only through a projected capability module.
	StatusUnknown uint16 = 16
)

// statusNames is the diagnostic rendering of every declared status.
//
// It is a map rather than a switch so that adding a status and forgetting its
// name is one missing key rather than a silently reachable default, and so
// that TestStatusName_CoversEveryDeclaredStatus can assert the vocabulary and
// this table have the same size.
var statusNames = map[uint16]string{
	StatusOK:                 "OK",
	StatusError:              "ERROR",
	StatusUnimplemented:      "UNIMPLEMENTED",
	StatusUnauthenticated:    "UNAUTHENTICATED",
	StatusPermissionDenied:   "PERMISSION_DENIED",
	StatusUnavailable:        "UNAVAILABLE",
	StatusNotFound:           "NOT_FOUND",
	StatusInvalidArgument:    "INVALID_ARGUMENT",
	StatusAlreadyExists:      "ALREADY_EXISTS",
	StatusFailedPrecondition: "FAILED_PRECONDITION",
	StatusResourceExhausted:  "RESOURCE_EXHAUSTED",
	StatusAborted:            "ABORTED",
	StatusOutOfRange:         "OUT_OF_RANGE",
	StatusCanceled:           "CANCELED",
	StatusDataLoss:           "DATA_LOSS",
	StatusDeadlineExceeded:   "DEADLINE_EXCEEDED",
	StatusUnknown:            "UNKNOWN",
}

// StatusName renders a wire status for logs and error messages. A status this
// build does not declare renders as "STATUS_<n>" rather than as a guess, so a
// frame from a newer peer is legible in a log without being misreported.
func StatusName(status uint16) string {
	if name, ok := statusNames[status]; ok {
		return name
	}
	return "STATUS_" + strconv.FormatUint(uint64(status), 10)
}

// StatusDeclared reports whether this build declares the given status. It is
// how a client tells "a condition I do not know yet" from "a condition I chose
// to handle generically".
func StatusDeclared(status uint16) bool {
	_, ok := statusNames[status]
	return ok
}

// ProtocolV2 is the TLS ALPN identifier of this frame protocol, and the only
// one a qrdb QUIC listener accepts: a peer that does not offer it fails the
// handshake. It replaced "qrdb-quic", whose frames were identical but which
// let the client half-close a request stream as soon as it had written the
// request.
//
// Under ProtocolV2 the client holds its write side open until it has READ the
// whole response frame, and only then closes it. That FIN is the client's
// acknowledgement that the response arrived, and the server waits for it
// (bounded) before it treats a request as finished. It is what lets a server
// that is stopping tell "my reply was delivered" from "my reply was queued",
// which quic-go gives it no other way to learn: closing a connection discards
// whatever it had queued but not yet sent. The old identifier is not accepted
// alongside it because a client that half-closes early sends a FIN that
// proves nothing, and a server cannot tell that FIN from an acknowledgement.
const ProtocolV2 = "qrdb-quic/2"

// MaxMethodLen is the maximum length of a method name in bytes.
const MaxMethodLen = 256

// MaxDatabaseLen is the maximum length of a database scope name in bytes.
const MaxDatabaseLen = 256

// MaxPayloadSize is the maximum payload size in bytes (64 MB).
const MaxPayloadSize = 64 * 1024 * 1024

// WriteRequest writes a request frame to the writer.
//
// Frame layout: [methodLen:uint16][method:string][dbLen:uint16][database:string][requestID:uint64][payloadLen:uint32][payload:bytes].
func WriteRequest(w io.Writer, method, database string, requestID uint64, payload []byte) error {
	methodBytes := []byte(method)
	methodLen := len(methodBytes)
	if methodLen > MaxMethodLen {
		return &MethodTooLongError{Length: methodLen, Max: MaxMethodLen}
	}

	dbBytes := []byte(database)
	dbLen := len(dbBytes)
	if dbLen > MaxDatabaseLen {
		return &DatabaseTooLongError{Length: dbLen, Max: MaxDatabaseLen}
	}

	payloadLen := len(payload)
	if payloadLen > MaxPayloadSize {
		return &PayloadTooLargeError{Size: payloadLen, Max: MaxPayloadSize}
	}

	// Preallocate header buffer:
	// 2 (methodLen) + methodLen + 2 (dbLen) + dbLen + 8 (requestID) + 4 (payloadLen)
	headerSize := 2 + methodLen + 2 + dbLen + 8 + 4
	buf := make([]byte, headerSize)

	// Method length (uint16)
	binary.BigEndian.PutUint16(buf[0:2], uint16(methodLen))

	// Method name
	copy(buf[2:2+methodLen], methodBytes)

	// Database length (uint16)
	offset := 2 + methodLen
	binary.BigEndian.PutUint16(buf[offset:offset+2], uint16(dbLen))

	// Database name
	offset += 2
	copy(buf[offset:offset+dbLen], dbBytes)

	// Request ID (uint64)
	offset += dbLen
	binary.BigEndian.PutUint64(buf[offset:offset+8], requestID)

	// Payload length (uint32)
	offset += 8
	binary.BigEndian.PutUint32(buf[offset:offset+4], uint32(payloadLen))

	// Write header
	if _, err := w.Write(buf); err != nil {
		return &WriteError{Err: err}
	}

	// Write payload
	if payloadLen > 0 {
		if _, err := w.Write(payload); err != nil {
			return &WriteError{Err: err}
		}
	}

	return nil
}

// ReadRequest reads a request frame from the reader.
//
// Returns the method name, database scope, request ID, payload bytes, and any
// error.
func ReadRequest(r io.Reader) (method, database string, requestID uint64, payload []byte, err error) {
	// Read method length (2 bytes)
	var methodLenBuf [2]byte
	if _, err = io.ReadFull(r, methodLenBuf[:]); err != nil {
		return "", "", 0, nil, &ReadError{Err: err}
	}
	methodLen := int(binary.BigEndian.Uint16(methodLenBuf[:]))

	if methodLen > MaxMethodLen {
		return "", "", 0, nil, &MethodTooLongError{Length: methodLen, Max: MaxMethodLen}
	}

	// Read method name
	methodBuf := make([]byte, methodLen)
	if _, err = io.ReadFull(r, methodBuf); err != nil {
		return "", "", 0, nil, &ReadError{Err: err}
	}
	method = string(methodBuf)

	// Read database length (2 bytes)
	var dbLenBuf [2]byte
	if _, err = io.ReadFull(r, dbLenBuf[:]); err != nil {
		return "", "", 0, nil, &ReadError{Err: err}
	}
	dbLen := int(binary.BigEndian.Uint16(dbLenBuf[:]))

	if dbLen > MaxDatabaseLen {
		return "", "", 0, nil, &DatabaseTooLongError{Length: dbLen, Max: MaxDatabaseLen}
	}

	// Read database name
	if dbLen > 0 {
		dbBuf := make([]byte, dbLen)
		if _, err = io.ReadFull(r, dbBuf); err != nil {
			return "", "", 0, nil, &ReadError{Err: err}
		}
		database = string(dbBuf)
	}

	// Read request ID (8 bytes)
	var ridBuf [8]byte
	if _, err = io.ReadFull(r, ridBuf[:]); err != nil {
		return "", "", 0, nil, &ReadError{Err: err}
	}
	requestID = binary.BigEndian.Uint64(ridBuf[:])

	// Read payload length (4 bytes)
	var plenBuf [4]byte
	if _, err = io.ReadFull(r, plenBuf[:]); err != nil {
		return "", "", 0, nil, &ReadError{Err: err}
	}
	payloadLen := int(binary.BigEndian.Uint32(plenBuf[:]))

	if payloadLen > MaxPayloadSize {
		return "", "", 0, nil, &PayloadTooLargeError{Size: payloadLen, Max: MaxPayloadSize}
	}

	// Read payload
	if payloadLen > 0 {
		payload = make([]byte, payloadLen)
		if _, err = io.ReadFull(r, payload); err != nil {
			return "", "", 0, nil, &ReadError{Err: err}
		}
	}

	return method, database, requestID, payload, nil
}

// WriteResponse writes a response frame to the writer.
//
// Frame layout: [requestID:uint64][statusCode:uint16][payloadLen:uint32][payload:bytes].
func WriteResponse(w io.Writer, requestID uint64, statusCode uint16, payload []byte) error {
	payloadLen := len(payload)
	if payloadLen > MaxPayloadSize {
		return &PayloadTooLargeError{Size: payloadLen, Max: MaxPayloadSize}
	}

	// Preallocate header: 8 (requestID) + 2 (statusCode) + 4 (payloadLen)
	var buf [14]byte
	binary.BigEndian.PutUint64(buf[0:8], requestID)
	binary.BigEndian.PutUint16(buf[8:10], statusCode)
	binary.BigEndian.PutUint32(buf[10:14], uint32(payloadLen))

	if _, err := w.Write(buf[:]); err != nil {
		return &WriteError{Err: err}
	}

	if payloadLen > 0 {
		if _, err := w.Write(payload); err != nil {
			return &WriteError{Err: err}
		}
	}

	return nil
}

// ReadResponse reads a response frame from the reader.
//
// Returns the request ID, status code, payload bytes, and any error.
func ReadResponse(r io.Reader) (requestID uint64, statusCode uint16, payload []byte, err error) {
	// Read header: 8 (requestID) + 2 (statusCode) + 4 (payloadLen)
	var buf [14]byte
	if _, err = io.ReadFull(r, buf[:]); err != nil {
		return 0, 0, nil, &ReadError{Err: err}
	}

	requestID = binary.BigEndian.Uint64(buf[0:8])
	statusCode = binary.BigEndian.Uint16(buf[8:10])
	payloadLen := int(binary.BigEndian.Uint32(buf[10:14]))

	if payloadLen > MaxPayloadSize {
		return 0, 0, nil, &PayloadTooLargeError{Size: payloadLen, Max: MaxPayloadSize}
	}

	if payloadLen > 0 {
		payload = make([]byte, payloadLen)
		if _, err = io.ReadFull(r, payload); err != nil {
			return 0, 0, nil, &ReadError{Err: err}
		}
	}

	return requestID, statusCode, payload, nil
}
