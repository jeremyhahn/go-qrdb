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

package server

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	grpctransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/grpc"
	mcptransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/mcp"
	quictransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/quic"
	resttransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/rest"
	unixtransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/unix"
)

// gapAdminService reports a capability profile carrying an at-rest gap.
type gapAdminService struct {
	mockAdminService
	gaps []string
}

func (g *gapAdminService) Capabilities(ctx context.Context) (*capabilities.Capabilities, error) {
	caps, err := g.mockAdminService.Capabilities(ctx)
	if err != nil {
		return nil, err
	}
	caps.UnencryptedAtRest = g.gaps
	return caps, nil
}

// capabilityClient is the capability query every SDK transport serves.
type capabilityClient interface {
	Capabilities(ctx context.Context) (*transport.CapabilitiesResponse, error)
}

// TestCapabilities_UnencryptedAtRest_EveryTransport pins the SEC-4/SEC-5
// report end to end: the node's at-rest gaps reach a client on REST, gRPC,
// Unix, QUIC and MCP alike.
func TestCapabilities_UnencryptedAtRest_EveryTransport(t *testing.T) {
	gaps := []string{`standalone backend "file" stores its data without the seal barrier's at-rest encryption`}
	cfg := testConfigFull(t, protocolFlags{rest: true, grpc: true, quic: true, unix: true, mcp: true}, filepath.Join(t.TempDir(), "qrdb.sock"))
	srv := New(cfg, newMockKVService(), &gapAdminService{gaps: gaps}, WithCeremonyCA(testServerCA))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	tlsOpt := testAutoProvisionClientTLSOption(t)
	builders := map[string]func() (transport.Transport, error){
		"rest": func() (transport.Transport, error) {
			return resttransport.New(transport.WithAddress("https://"+srv.RESTAddr()), tlsOpt)
		},
		"grpc": func() (transport.Transport, error) {
			return grpctransport.New(transport.WithAddress(srv.GRPCAddr()), tlsOpt)
		},
		"unix": func() (transport.Transport, error) {
			return unixtransport.New(transport.WithAddress(srv.UnixAddr()), tlsOpt)
		},
		"quic": func() (transport.Transport, error) {
			return quictransport.New(transport.WithAddress(srv.QUICAddr()), tlsOpt)
		},
		"mcp": func() (transport.Transport, error) {
			return mcptransport.New(transport.WithAddress("https://"+srv.MCPAddr()+"/mcp"), tlsOpt)
		},
	}
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			tr, err := build()
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := tr.Connect(context.Background()); err != nil {
				t.Fatalf("Connect: %v", err)
			}
			defer func() { _ = tr.Close() }()
			cc, ok := tr.(capabilityClient)
			if !ok {
				t.Fatalf("%T does not serve Capabilities", tr)
			}
			resp, err := cc.Capabilities(context.Background())
			if err != nil {
				t.Fatalf("Capabilities: %v", err)
			}
			if !reflect.DeepEqual(resp.UnencryptedAtRest, gaps) {
				t.Fatalf("UnencryptedAtRest = %q, want %q", resp.UnencryptedAtRest, gaps)
			}
		})
	}
}
