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

package grpc

import (
	"google.golang.org/grpc"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// RegisterService registers the QRDBServiceServer on the given gRPC server
// using the provided KV and admin service implementations. This allows
// other server transports (e.g., Unix domain sockets) to reuse the same
// service implementation without duplicating the protobuf glue code.
func RegisterService(srv *grpc.Server, kvSvc service.KVServicer, adminSvc service.AdminServicer) {
	svc := &qrdbServiceServer{
		kvSvc:    kvSvc,
		adminSvc: adminSvc,
	}
	pb.RegisterQRDBServiceServer(srv, svc)
}
