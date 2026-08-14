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
)

// Status codes returned in QUIC response frames.
const (
	StatusOK               uint16 = 0
	StatusError            uint16 = 1
	StatusUnimplemented    uint16 = 2
	StatusUnauthenticated  uint16 = 3
	StatusPermissionDenied uint16 = 4
	StatusUnavailable      uint16 = 5
)

// MaxMethodLen is the maximum length of a method name in bytes.
const MaxMethodLen = 256

// MaxDatabaseLen is the maximum length of a database scope name in bytes.
const MaxDatabaseLen = 256

// MaxPayloadSize is the maximum payload size in bytes (64 MB).
const MaxPayloadSize = 64 * 1024 * 1024

// WriteRequest writes a request frame to the writer.
//
// Frame layout: [methodLen:uint16][method:string][dbLen:uint16][database:string][requestID:uint64][payloadLen:uint32][payload:bytes]
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
// Frame layout: [requestID:uint64][statusCode:uint16][payloadLen:uint32][payload:bytes]
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
