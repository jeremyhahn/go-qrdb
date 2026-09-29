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

package quic

import (
	"context"

	quicgo "github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
)

// soPINField is the request-message field that carries the SO PIN on QUIC.
const soPINField = "so_pin"

// soPINCarriers names, for every SO-gated QUIC method, a constructor for its
// request message, whose so_pin field carries the SO PIN.
//
// A QUIC frame has no header section, so unlike REST (the X-QRDB-SO-PIN
// header), gRPC and Unix (x-qrdb-so-pin metadata) and MCP (the header on the
// /mcp request), the only per-request carrier QUIC has is the request message
// itself. barrier.RecoveryDelete's request used to be empty and so had no
// carrier; it is now BarrierRecoveryDeleteRequest.
//
// A gated method absent from this table would present no PIN and the gate
// would refuse it (fail-closed), never admit it.
// TestSOGate_EveryGatedQUICMethodHasACarrier pins the table against the
// method registry, so a newly gated method cannot silently lack one.
var soPINCarriers = map[string]func() proto.Message{
	"barrier.ShamirInitialize": func() proto.Message { return &pb.BarrierShamirInitializeRequest{} },
	"barrier.RekeyStart":       func() proto.Message { return &pb.BarrierRekeyStartRequest{} },
	"barrier.OfficerRevoke":    func() proto.Message { return &pb.BarrierOfficerRevokeRequest{} },
	"barrier.RecoveryGenerate": func() proto.Message { return &pb.BarrierRecoveryGenerateRequest{} },
	"barrier.RecoveryDelete":   func() proto.Message { return &pb.BarrierRecoveryDeleteRequest{} },
	"ceremony.Initialize":      func() proto.Message { return &pb.CeremonyInitializeRequest{} },
	"ceremony.SignCSR":         func() proto.Message { return &pb.CeremonySignCSRRequest{} },
}

// soPINFromPayload returns the SO PIN a gated method's request carries, or
// nil when the method has no carrier or the payload does not decode.
func soPINFromPayload(method string, payload []byte) []byte {
	newMsg, ok := soPINCarriers[method]
	if !ok {
		return nil
	}
	msg := newMsg()
	if proto.Unmarshal(payload, msg) != nil {
		return nil
	}
	refl := msg.ProtoReflect()
	fd := refl.Descriptor().Fields().ByName(soPINField)
	if fd == nil {
		return nil
	}
	if pin := refl.Get(fd).String(); pin != "" {
		return []byte(pin)
	}
	return nil
}

// enforceSOGate applies the SO dual-factor gate to one request, after
// authorization. QUIC is TLS-only, so the factor always arrives over TLS.
func (s *Server) enforceSOGate(ctx context.Context, conn *quicgo.Conn, method string, payload []byte) error {
	perm, ok := s.methodPerms.Lookup(method)
	if !ok {
		return nil
	}
	if _, gated := authmw.SOPermissions[perm]; !gated {
		return nil
	}
	factor := authmw.NewSOFactor(soPINFromPayload(method, payload), true, conn.RemoteAddr().String())
	defer factor.Destroy()
	return s.soGate.Enforce(authmw.WithSOFactor(ctx, factor), perm, method, "call")
}
