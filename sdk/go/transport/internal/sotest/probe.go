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

package sotest

import (
	"context"
	"sync"

	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Probe is a QRDBService that answers Health and BarrierRekey and records
// every SO PIN that reaches it in metadata, so a test can assert that one
// never crossed a connection it must not cross. It stands in for a server the
// client cannot trust -- a plaintext listener -- which is why it is not the
// real server: the real one refuses plaintext before the PIN would matter,
// and the property under test is what the CLIENT sends.
type Probe struct {
	pb.UnimplementedQRDBServiceServer
	mu   sync.Mutex
	pins []string
}

// Health reports healthy, so the SDK's Connect succeeds.
func (p *Probe) Health(context.Context, *emptypb.Empty) (*pb.HealthResponse, error) {
	return &pb.HealthResponse{Status: "ok"}, nil
}

// BarrierRekey records any SO PIN the request carried.
func (p *Probe) BarrierRekey(ctx context.Context, _ *pb.BarrierRekeyRequest) (*pb.BarrierShamirOperationResponse, error) {
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		p.mu.Lock()
		p.pins = append(p.pins, md.Get(transport.SOPINMetadataKey)...)
		p.mu.Unlock()
	}
	return &pb.BarrierShamirOperationResponse{Threshold: 2, TotalShares: 3}, nil
}

// SawPIN reports whether any SO PIN reached the probe.
func (p *Probe) SawPIN() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pins) > 0
}
