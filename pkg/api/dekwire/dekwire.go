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
// wire shapes. It is the one conversion every transport shares, so they
// cannot drift apart. Error classification is not here: keyring errors are
// rows in pkg/api/errclass, like every other condition.
package dekwire

import (
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

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
			Version:    v.Version,
			CreatedAt:  v.CreatedAt,
			RetiredAt:  v.RetiredAt,
			ProviderID: v.ProviderID,
			KeyRef:     v.KeyRef,
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
		pv := &pb.TenantDEKVersion{
			Version:       v.Version,
			CreatedAtUnix: v.CreatedAt.Unix(),
			ProviderId:    v.ProviderID,
			KeyRef:        v.KeyRef,
		}
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
