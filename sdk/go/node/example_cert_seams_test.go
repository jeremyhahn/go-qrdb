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

package node_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
	sdkserver "github.com/jeremyhahn/go-qrdb/sdk/go/server"
)

// exampleCA is an embedder's own login CA, the seam node.WithCertProvider
// takes. A real one issues from its own key custody and publishes CRLs.
type exampleCA struct{}

func (exampleCA) IssueClientCert(context.Context, *sdk.ClientCertIssueRequest) (*sdk.ClientCertIssued, error) {
	return nil, &sdk.CAKeyNotInitializedError{} // no tenant key enrolled in this example
}

func (exampleCA) RevokeCert(context.Context, string) (*sdk.ClientCertRevokeResult, error) {
	return &sdk.ClientCertRevokeResult{}, nil
}

func (exampleCA) IsRevoked(context.Context, string) (bool, error) { return false, nil }

func (exampleCA) TenantForSerial(context.Context, string) (string, bool, error) {
	return "", false, nil
}

func (exampleCA) ListCerts(_ context.Context, tenantID string) ([]*sdk.ClientCertRecord, error) {
	return []*sdk.ClientCertRecord{{TenantID: tenantID, Username: "issued by the example login CA"}}, nil
}

// exampleKeyB is a per-tenant Key B factory backing no tenant yet, the seam
// node.WithCertKeyProviderFactory takes. A real one opens one device-held key
// per tenant. The node's client-trust pool enumerates it from its own
// goroutine, so its state is atomic, as a real one's must be.
type exampleKeyB struct{ enumerated atomic.Bool }

func (*exampleKeyB) ProviderForTenant(string) (sdk.CAKeyProvider, error) {
	return nil, &sdk.CAKeyNotInitializedError{}
}

func (f *exampleKeyB) TenantIDs(context.Context) ([]string, error) {
	f.enumerated.Store(true)
	return nil, nil
}

// openExampleNode opens a sealed in-process node with no listeners in a
// throwaway directory.
func openExampleNode(opts ...node.NodeOption) (*node.Node, func(), error) {
	dir, err := os.MkdirTemp("", "qrdb-example-")
	if err != nil {
		return nil, nil, err
	}
	cfg := sdk.DefaultDaemonConfig()
	cfg.Raft.DataDir = dir
	cfg.Databases = []sdk.DaemonDatabaseConfig{{Name: "default", Backend: string(sdk.BackendMemory)}}
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Auth.Enabled = sdk.BoolPtr(false)
	n, err := node.NewStandaloneNode(context.Background(), cfg, nil, opts...)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, nil, err
	}
	return n, func() { _ = n.Close(); _ = os.RemoveAll(dir) }, nil
}

// ExampleWithCertProvider replaces the node's built-in login CA with the
// embedder's own; every transport's certificate operations are then served by
// it.
func ExampleWithCertProvider() {
	n, closeNode, err := openExampleNode(node.WithCertProvider(exampleCA{}))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer closeNode()

	recs, err := n.Services().CertProv.ListCerts(context.Background(), "acme")
	fmt.Println(recs[0].Username, err)
	// Output: issued by the example login CA <nil>
}

// ExampleWithCertKeyProviderFactory keeps the built-in login CA but signs each
// tenant's client certificates with a key the embedder holds, one per tenant.
func ExampleWithCertKeyProviderFactory() {
	keys := &exampleKeyB{}
	n, closeNode, err := openExampleNode(node.WithCertKeyProviderFactory(keys))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer closeNode()

	src, ok := n.Services().CertProv.(sdk.ClientCertSource)
	if !ok {
		fmt.Println("the built-in login CA is not a ClientCertSource")
		return
	}
	_, err = src.CACertPEMs(context.Background())
	fmt.Println(keys.enumerated.Load(), err)
	// Output: true <nil>
}

// TestCertSeams_ConflictIsTheSDKError holds the error an embedder sees for
// both cert seams to the name it can import.
func TestCertSeams_ConflictIsTheSDKError(t *testing.T) {
	n, closeNode, err := openExampleNode(node.WithCertProvider(exampleCA{}), node.WithCertKeyProviderFactory(&exampleKeyB{}))
	if n != nil {
		closeNode()
	}
	var conflict *sdkserver.CertProviderConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("both cert seams = %v, want *sdkserver.CertProviderConflictError", err)
	}
}

// TestCertSeams_OneSeamIsNoConflict is the control: either seam alone builds.
func TestCertSeams_OneSeamIsNoConflict(t *testing.T) {
	for _, opt := range []node.NodeOption{node.WithCertProvider(exampleCA{}), node.WithCertKeyProviderFactory(&exampleKeyB{})} {
		_, closeNode, err := openExampleNode(opt)
		if err != nil {
			t.Fatalf("one cert seam: %v", err)
		}
		closeNode()
	}
}
