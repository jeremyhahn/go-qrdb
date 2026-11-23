//go:build integration

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

package integration_test

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	connectrpc "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	qrdbnode "github.com/jeremyhahn/go-qrdb/sdk/go/node"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	grpctransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/grpc"
	mcptransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/mcp"
	quictransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/quic"
	resttransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/rest"
	unixtransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport/unix"
)

// sealNodeAdminClient returns an SDK client for proto on node, authenticated
// with the admin credential `qrdb init` issued.
func sealNodeAdminClient(t *testing.T, node *sealNodeAllTransports, proto multiProtocol) *sdk.Client {
	t.Helper()
	opts := []transport.Option{transport.WithMTLS(node.adminCert, node.adminKey, node.caFile)}
	var (
		tr  transport.Transport
		err error
	)
	switch proto.name {
	case "rest":
		tr, err = resttransport.New(append(opts, transport.WithAddress(node.serverURLFor(proto)))...)
	case "grpc":
		tr, err = grpctransport.New(append(opts, transport.WithAddress(fmt.Sprintf("127.0.0.1:%d", node.grpcPort)))...)
	case "quic":
		tr, err = quictransport.New(append(opts, transport.WithAddress(fmt.Sprintf("127.0.0.1:%d", node.quicPort)))...)
	case "mcp":
		tr, err = mcptransport.New(append(opts, transport.WithAddress(fmt.Sprintf("https://127.0.0.1:%d/mcp", node.mcpPort)))...)
	case "unix":
		tr, err = unixtransport.New(append(opts, transport.WithAddress(node.unixSocket))...)
	default:
		t.Fatalf("unknown protocol %q", proto.name)
	}
	if err != nil {
		t.Fatalf("[%s] transport: %v", proto.name, err)
	}
	client, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("[%s] client: %v", proto.name, err)
	}
	return client
}

// assertSealStoreSDKRefused calls every Client.SealStore() verb and requires
// each to fail with *transport.EmbeddedOnlyError naming the verb.
func assertSealStoreSDKRefused(t *testing.T, ctx context.Context, client *sdk.Client, proto multiProtocol, phase string) {
	t.Helper()
	store := client.SealStore()
	calls := map[string]func() error{
		transport.OpSealStorePut: func() error {
			return store.SealStorePut(ctx, &transport.SealStorePutRequest{Name: "alpha", Secret: []byte("seal-store-sdk-secret")})
		},
		transport.OpSealStoreGet: func() error {
			_, err := store.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "alpha"})
			return err
		},
		transport.OpSealStoreDelete: func() error {
			return store.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{Name: "alpha"})
		},
		transport.OpSealStoreList: func() error {
			_, err := store.SealStoreList(ctx)
			return err
		},
		transport.OpSealStoreReseal: func() error {
			return store.SealStoreReseal(ctx, &transport.SealStoreResealRequest{Name: "alpha"})
		},
		transport.OpSealStoreStatus: func() error {
			_, err := store.SealStoreStatus(ctx)
			return err
		},
	}
	for _, op := range transport.EmbeddedOnlyOperations {
		call, ok := calls[op]
		if !ok {
			t.Fatalf("[%s %s] no call for embedded-only operation %s", proto.name, phase, op)
		}
		err := call()
		if !errors.Is(err, transport.ErrEmbeddedOnly) {
			t.Fatalf("[%s %s] %s = %v, want transport.ErrEmbeddedOnly", proto.name, phase, op, err)
		}
		var eo *transport.EmbeddedOnlyError
		if !errors.As(err, &eo) || eo.Operation != op || eo.Transport != proto.name {
			t.Fatalf("[%s %s] %s = %v, want *transport.EmbeddedOnlyError{%s, %s}", proto.name, phase, op, err, op, proto.name)
		}
	}
}

// TestSealStore_SDK_EmbeddedOnly_PerProtocol: over every network transport
// each Client.SealStore() verb is refused as embedded-only (ruling 1234),
// while Client.Barrier().BarrierSeal and BarrierUnseal keep working on the
// same transport; the verbs stay refused after the seal/unseal cycle.
func TestSealStore_SDK_EmbeddedOnly_PerProtocol(t *testing.T) {
	t.Parallel()
	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			t.Parallel()
			node, _ := startEmbeddedOnlySealNode(t, proto)
			client := sealNodeAdminClient(t, node, proto)
			defer client.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			if err := client.Connect(ctx); err != nil {
				t.Fatalf("[%s] connect: %v", proto.name, err)
			}

			assertSealStoreSDKRefused(t, ctx, client, proto, "unsealed")

			sealCtx, release, err := transport.WithSOPIN(ctx, []byte(sealNodeSOPIN))
			if err != nil {
				t.Fatalf("[%s] WithSOPIN: %v", proto.name, err)
			}
			err = client.Barrier().BarrierSeal(sealCtx)
			release()
			if err != nil {
				t.Fatalf("[%s] BarrierSeal: %v", proto.name, err)
			}
			if err := client.Barrier().BarrierUnseal(ctx, &transport.BarrierUnsealRequest{Secret: []byte(embeddedOnlySealPassphrase)}); err != nil {
				t.Fatalf("[%s] BarrierUnseal: %v", proto.name, err)
			}
			status, err := client.Barrier().BarrierStatus(ctx)
			if err != nil {
				t.Fatalf("[%s] BarrierStatus after unseal: %v", proto.name, err)
			}
			if status.Sealed {
				t.Fatalf("[%s] BarrierStatus after unseal = %+v, want unsealed", proto.name, status)
			}

			assertSealStoreSDKRefused(t, ctx, client, proto, "after unseal")
		})
	}
}

// embeddedSealStorePassphrase is the passphrase fixture of the embedded
// seal-store lifecycle node: WithSealPassphrase initializes the node with it
// and every BarrierUnseal below presents it.
const embeddedSealStorePassphrase = "embedded-seal-store-passphrase-1647"

// TestSealStore_SDK_Embedded_Lifecycle proves the one surface that serves
// seal-store data (ruling 1234): an in-process passphrase-sealed node's
// embedded client puts, gets, lists, reports, reseals and deletes a sealed
// secret through Client.SealStore(); with the barrier sealed every verb is
// refused by the node's sealed gate (seal.ErrSealed), and
// after BarrierUnseal the stored secret reads back unchanged.
func TestSealStore_SDK_Embedded_Lifecycle(t *testing.T) {
	t.Parallel()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = string(seal.StrategyPassphrase)
	cfg.Seal.RootKeyPath = "root-key"
	cfg.Seal.CredentialStrategy = "barrier"
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	// The suite drives the seal store's data path in process; seal-store
	// authorization (permission.SealStoreManage) has its own coverage in
	// sdk/go/transport/embedded.
	cfg.Auth.Enabled = qrdbconfig.BoolPtr(false)

	n, err := qrdbnode.NewStandaloneNode(context.Background(), cfg, nil,
		qrdbnode.WithSealPassphrase([]byte(embeddedSealStorePassphrase)))
	if err != nil {
		t.Fatalf("NewStandaloneNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	if n.IsSealed() {
		t.Fatal("the passphrase node booted sealed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client := n.Client()
	store := client.SealStore()
	secret := []byte("embedded-seal-store-secret")

	if err := store.SealStorePut(ctx, &transport.SealStorePutRequest{Name: "alpha", Secret: secret}); err != nil {
		t.Fatalf("SealStorePut: %v", err)
	}
	got, err := store.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "alpha"})
	if err != nil {
		t.Fatalf("SealStoreGet: %v", err)
	}
	if got.Name != "alpha" || string(got.Secret) != string(secret) {
		t.Fatalf("SealStoreGet = {%q, %q}, want {alpha, %q}", got.Name, got.Secret, secret)
	}
	list, err := store.SealStoreList(ctx)
	if err != nil {
		t.Fatalf("SealStoreList: %v", err)
	}
	if len(list.Names) != 1 || list.Names[0] != "alpha" {
		t.Fatalf("SealStoreList = %v, want [alpha]", list.Names)
	}
	status, err := store.SealStoreStatus(ctx)
	if err != nil {
		t.Fatalf("SealStoreStatus: %v", err)
	}
	if !status.Available || status.SecretCount != 1 {
		t.Fatalf("SealStoreStatus = %+v, want available with 1 secret", status)
	}
	if err := store.SealStoreReseal(ctx, &transport.SealStoreResealRequest{Name: "alpha"}); err != nil {
		t.Fatalf("SealStoreReseal: %v", err)
	}
	if got, err := store.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "alpha"}); err != nil || string(got.Secret) != string(secret) {
		t.Fatalf("SealStoreGet after reseal = %v, %v; want %q", got, err, secret)
	}
	if err := store.SealStorePut(ctx, &transport.SealStorePutRequest{Name: "beta", Secret: []byte("second")}); err != nil {
		t.Fatalf("SealStorePut beta: %v", err)
	}
	if err := store.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{Name: "beta"}); err != nil {
		t.Fatalf("SealStoreDelete: %v", err)
	}
	if _, err := store.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "beta"}); !errors.Is(err, sdk.ErrSealStoreSecretNotFound) {
		t.Fatalf("SealStoreGet after delete = %v, want sdk.ErrSealStoreSecretNotFound", err)
	}

	if err := client.Barrier().BarrierSeal(ctx); err != nil {
		t.Fatalf("BarrierSeal: %v", err)
	}
	sealedCalls := map[string]func() error{
		transport.OpSealStorePut: func() error {
			return store.SealStorePut(ctx, &transport.SealStorePutRequest{Name: "gamma", Secret: []byte("x")})
		},
		transport.OpSealStoreGet: func() error {
			_, err := store.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "alpha"})
			return err
		},
		transport.OpSealStoreDelete: func() error {
			return store.SealStoreDelete(ctx, &transport.SealStoreDeleteRequest{Name: "alpha"})
		},
		transport.OpSealStoreList: func() error {
			_, err := store.SealStoreList(ctx)
			return err
		},
		transport.OpSealStoreReseal: func() error {
			return store.SealStoreReseal(ctx, &transport.SealStoreResealRequest{Name: "alpha"})
		},
	}
	for op, call := range sealedCalls {
		// The node's embedded seal gate refuses before the store runs, so
		// the refusal is the barrier's own seal.ErrSealed; the store's
		// sdk.ErrSealStoreBarrierSealed is its in-store guard behind it.
		if err := call(); !errors.Is(err, seal.ErrSealed) {
			t.Fatalf("sealed %s = %v, want seal.ErrSealed", op, err)
		}
	}

	if err := client.Barrier().BarrierUnseal(ctx, &transport.BarrierUnsealRequest{Secret: []byte(embeddedSealStorePassphrase)}); err != nil {
		t.Fatalf("BarrierUnseal: %v", err)
	}
	got, err = store.SealStoreGet(ctx, &transport.SealStoreGetRequest{Name: "alpha"})
	if err != nil {
		t.Fatalf("SealStoreGet after unseal: %v", err)
	}
	if string(got.Secret) != string(secret) {
		t.Fatalf("SealStoreGet after unseal = %q, want %q", got.Secret, secret)
	}
}

// removedSealStoreMethods are the qrdb.v1 seal-store procedures the row
// removed from every remote transport (ruling 1234). Their generated method
// constants are gone with them, so the names are spelled here.
var removedSealStoreMethods = []string{
	"/qrdb.v1.QRDBService/SealStorePut",
	"/qrdb.v1.QRDBService/SealStoreGet",
	"/qrdb.v1.QRDBService/SealStoreDelete",
	"/qrdb.v1.QRDBService/SealStoreList",
	"/qrdb.v1.QRDBService/SealStoreReseal",
	"/qrdb.v1.QRDBService/SealStoreStatus",
}

// TestSealStore_GRPCWeb_EmbeddedOnly closes the gRPC-Web leg of the
// embedded-only refusal: on a passphrase-sealed node with gRPC-Web enabled,
// every removed seal-store procedure called through /grpc-web/ on the REST
// listener answers Unimplemented with no data, unsealed and sealed, and
// BarrierUnseal (the passphrase fixture) over the same route unseals.
func TestSealStore_GRPCWeb_EmbeddedOnly(t *testing.T) {
	t.Parallel()
	binary := buildBinary(t)
	cli := buildCLIBinary(t)
	rest := multiProtocols[0]

	configPath := writeSealConfig(t, "passphrase", 0, 0)
	f, err := os.OpenFile(configPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open seal config: %v", err)
	}
	if _, err := f.WriteString("server:\n  grpc_web:\n    enabled: true\n"); err != nil {
		t.Fatalf("append grpc_web config: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close seal config: %v", err)
	}
	sockDir, err := os.MkdirTemp("", "qrdb-gw-")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	raftPorts := allocatePorts(t, 1)
	quicPorts := allocatePorts(t, 1)
	tcpPorts := allocateTCPPorts(t, 3)
	node := startSealNodeAllTransports(t, binary, t.TempDir(), configPath,
		raftPorts[0], tcpPorts[0], tcpPorts[1], quicPorts[0], tcpPorts[2], filepath.Join(sockDir, "qrdbd.sock"))
	waitForBarrierReachable(t, cli, node.noAuthFlagsFor(rest), 60*time.Second)
	node.initSystem(t, cli, rest, "--barrier-strategy", "passphrase", "--passphrase", embeddedOnlySealPassphrase)
	node.waitForFullBoot(t)

	cert, err := tls.LoadX509KeyPair(node.adminCert, node.adminKey)
	if err != nil {
		t.Fatalf("load admin certificate: %v", err)
	}
	tr := &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: node.ca.Pool, Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13},
		ForceAttemptHTTP2: true,
	}
	defer tr.CloseIdleConnections()
	base := fmt.Sprintf("https://127.0.0.1:%d/grpc-web", node.adminPort)
	client := connectrpc.NewClient(connecthttp.NewTransport(&http.Client{Transport: tr}, base, connecthttp.WithGRPCWeb()))
	call := func(procedure string, header map[string]string, req, resp any) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		ctx, info := connectrpc.NewClientContext(ctx)
		for k, v := range header {
			info.RequestHeader().Set(k, v)
		}
		return client.CallUnary(ctx, connectrpc.Spec{StreamType: connectrpc.StreamTypeUnary, Procedure: procedure}, req, resp)
	}

	assertRefused := func(phase string) {
		t.Helper()
		for _, procedure := range removedSealStoreMethods {
			resp := &emptypb.Empty{}
			err := call(procedure, nil, &emptypb.Empty{}, resp)
			if got := codes.Code(connectrpc.CodeOf(err)); got != codes.Unimplemented {
				t.Fatalf("[%s] gRPC-Web %s = %v (%v), want Unimplemented", phase, procedure, got, err)
			}
		}
	}
	assertRefused("unsealed")

	// BarrierSeal over gRPC-Web carries the barrier SO PIN exactly as the
	// native gRPC SDK does: in BarrierSealRequest.so_pin, which the handler
	// checks with pin.RequireSOPIN. seal:seal is not an SO-gate permission
	// (authmw.SOPermissions), so no x-qrdb-so-pin header is read for it on
	// any transport. A request without the PIN is refused, and the unseal is
	// the call a sealed node must still serve.
	if err := call(pb.QRDBService_BarrierSeal_FullMethodName, nil,
		&pb.BarrierSealRequest{}, &emptypb.Empty{}); err == nil {
		t.Fatal("gRPC-Web BarrierSeal without the SO PIN succeeded, want the barrier PIN refusal")
	}
	if err := call(pb.QRDBService_BarrierSeal_FullMethodName, nil,
		&pb.BarrierSealRequest{SoPin: sealNodeSOPIN}, &emptypb.Empty{}); err != nil {
		t.Fatalf("gRPC-Web BarrierSeal: %v", err)
	}
	sealed := &pb.BarrierStatusResponse{}
	if err := call(pb.QRDBService_BarrierStatus_FullMethodName, nil, &emptypb.Empty{}, sealed); err != nil || !sealed.GetSealed() {
		t.Fatalf("gRPC-Web BarrierStatus after seal = %+v, %v; want sealed", sealed, err)
	}
	assertRefused("sealed")
	if err := call(pb.QRDBService_BarrierUnseal_FullMethodName, nil,
		&pb.BarrierUnsealRequest{Secret: []byte(embeddedOnlySealPassphrase)}, &emptypb.Empty{}); err != nil {
		t.Fatalf("gRPC-Web BarrierUnseal: %v", err)
	}
	unsealed := &pb.BarrierStatusResponse{}
	if err := call(pb.QRDBService_BarrierStatus_FullMethodName, nil, &emptypb.Empty{}, unsealed); err != nil || unsealed.GetSealed() {
		t.Fatalf("gRPC-Web BarrierStatus after unseal = %+v, %v; want unsealed", unsealed, err)
	}
	assertRefused("after unseal")
}
