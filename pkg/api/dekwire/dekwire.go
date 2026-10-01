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

// Package dekwire converts tenant DEK keyring results to their transport
// wire shapes. It is the one conversion every JSON transport (REST, QUIC,
// MCP) and the embedded transport share, so they cannot drift apart.
package dekwire

import (
	"errors"

	"google.golang.org/grpc/codes"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// codeByClass maps each keyring error class to its canonical status code.
// Every transport renders that code in its own vocabulary — gRPC directly,
// QUIC and MCP through their gRPC-code correspondence tables, REST through
// HTTPStatus — so a condition means the same thing on every wire.
var codeByClass = []struct {
	class error
	code  codes.Code
}{
	{dek.ErrInvalidInput, codes.InvalidArgument},
	{dek.ErrCrossTenant, codes.PermissionDenied},
	{dek.ErrMalformedCiphertext, codes.InvalidArgument},
	{dek.ErrAuthenticationFailed, codes.InvalidArgument},
	{dek.ErrUnknownVersion, codes.NotFound},
	{dek.ErrVersionRetired, codes.FailedPrecondition},
	{dek.ErrRetireCurrentVersion, codes.FailedPrecondition},
	{dek.ErrKeyringUnavailable, codes.Unavailable},
	{dek.ErrCorruptKeyring, codes.DataLoss},
	{dek.ErrConcurrentUpdate, codes.Aborted},
	{dek.ErrCrypto, codes.Internal},
}

// Code classifies a keyring error. ok is false for an error that is not
// from the keyring, leaving the caller's own classification in charge. The
// classes are mutually exclusive, so the table order does not matter.
func Code(err error) (code codes.Code, ok bool) {
	for _, m := range codeByClass {
		if errors.Is(err, m.class) {
			return m.code, true
		}
	}
	return codes.Unknown, false
}

// httpStatusByCode renders the codes Code produces as HTTP statuses.
var httpStatusByCode = map[codes.Code]int{
	codes.InvalidArgument:    400,
	codes.PermissionDenied:   403,
	codes.NotFound:           404,
	codes.FailedPrecondition: 409,
	codes.Aborted:            409,
	codes.Unavailable:        503,
	codes.DataLoss:           500,
	codes.Internal:           500,
}

// HTTPStatus classifies a keyring error as an HTTP status. ok is false for an
// error that is not from the keyring.
func HTTPStatus(err error) (int, bool) {
	code, ok := Code(err)
	if !ok {
		return 0, false
	}
	return httpStatusByCode[code], true
}

// Status converts keyring status to its wire form. Versions is never nil, so
// a tenant with no keyring encodes as an empty list rather than null.
func Status(st *dek.Status) *transport.TenantDEKStatusResponse {
	out := &transport.TenantDEKStatusResponse{
		TenantID:       st.TenantID,
		Algorithm:      st.Algorithm,
		CurrentVersion: st.Current,
		Versions:       make([]transport.TenantDEKVersion, 0, len(st.Versions)),
	}
	for _, v := range st.Versions {
		out.Versions = append(out.Versions, transport.TenantDEKVersion{
			Version:   v.Version,
			CreatedAt: v.CreatedAt,
			RetiredAt: v.RetiredAt,
		})
	}
	return out
}

// StatusProto converts keyring status to its protobuf form, used by gRPC,
// Unix and QUIC.
func StatusProto(st *dek.Status) *pb.TenantDEKStatusResponse {
	out := &pb.TenantDEKStatusResponse{
		TenantId:       st.TenantID,
		Algorithm:      st.Algorithm,
		CurrentVersion: st.Current,
		Versions:       make([]*pb.TenantDEKVersion, 0, len(st.Versions)),
	}
	for _, v := range st.Versions {
		pv := &pb.TenantDEKVersion{Version: v.Version, CreatedAtUnix: v.CreatedAt.Unix()}
		if v.RetiredAt != nil {
			pv.RetiredAtUnix = v.RetiredAt.Unix()
		}
		out.Versions = append(out.Versions, pv)
	}
	return out
}

// Encrypt converts an Encrypt result to its wire form.
func Encrypt(s *dek.Sealed) *transport.TenantDEKEncryptResponse {
	return &transport.TenantDEKEncryptResponse{Ciphertext: s.Ciphertext, Version: s.Version}
}

// Decrypt converts a Decrypt result to its wire form.
func Decrypt(o *dek.Opened) *transport.TenantDEKDecryptResponse {
	return &transport.TenantDEKDecryptResponse{Plaintext: o.Plaintext, Version: o.Version}
}

// Rewrap converts a Rewrap result to its wire form.
func Rewrap(r *dek.Rewrapped) *transport.TenantDEKRewrapResponse {
	return &transport.TenantDEKRewrapResponse{
		Ciphertext:  r.Ciphertext,
		FromVersion: r.FromVersion,
		ToVersion:   r.ToVersion,
	}
}
