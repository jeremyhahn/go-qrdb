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

import (
	"errors"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// mustMarshal marshals m or panics. Used in test responders to keep them
// readable when canned protobuf responses are required.
func mustMarshal(m proto.Message) []byte {
	b, err := proto.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

// mustMarshalHealthOK returns a minimal HealthResponse{Status:"ok"} payload.
// Used by the harness so the SDK's Connect() health check succeeds.
func mustMarshalHealthOK() []byte {
	return mustMarshal(&pb.HealthResponse{Status: "ok", Leader: true})
}

// errorAs is a thin wrapper around errors.As that returns the boolean
// result, kept here so tests read cleanly without repeated `var ce *T`
// boilerplate. The target argument must be a non-nil pointer-to-pointer
// to a concrete error type, matching the errors.As contract.
func errorAs(err error, target any) bool {
	return errors.As(err, target)
}

// stubDocResponse returns a *pb.DocumentResponse with the supplied
// Document string and id. Used by tests that need to exercise the
// SDK's JSON-document unmarshal path after a successful proto unmarshal.
func stubDocResponse(document string, id uint64) *pb.DocumentResponse {
	return &pb.DocumentResponse{Id: id, Document: document}
}
