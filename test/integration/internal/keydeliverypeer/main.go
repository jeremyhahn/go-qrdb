// Copyright (c) 2026 Jeremy Hahn
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

// Command keydeliverypeer is a test peer for the rolling-upgrade
// integration suite: one half of a real cross-process HPKE tenant-key
// delivery. The suite builds it twice, at two release stamps, and runs a
// receiver from one build against a sender from the other, so the
// envelope's ReleaseVersion, its HPKE info binding and the HTTP exchange
// are all exercised across two binaries that differ only in release.
//
//	keydeliverypeer receive --listen 127.0.0.1:0 --tls-dir <dir>
//	keydeliverypeer send --to <url> --tls-dir <dir>
//
// The receiver runs a real clusterkeys.Receiver behind TLS as node
// follower-node-1, prints {"url":...} once it listens, then serves one
// delivery attempt and prints its outcome as one JSON line. The sender
// initializes a tenant barrier sealed by its owner strategy (not one of
// the own-key-on-every-node strategies DistributeTenantKey skips), ships
// it to the receiver as leader-node-1 and prints its fingerprint and the
// send result as one JSON line. A fingerprint is the SHA-256 of the
// barrier's exported sealed blob: equal fingerprints mean the receiver
// holds exactly the barrier the sender shipped.
//
// The TLS directory holds ca.pem plus <node>.pem and <node>-key.pem for
// each node, issued by the test from its own CA.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterkeys"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/release"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

const (
	trustDomain  = "cluster.local"
	leaderNode   = "leader-node-1"
	followerNode = "follower-node-1"
	tenantID     = "tenant-kd"
	database     = "db-main"
)

// result is the one JSON line each mode prints when it is done.
type result struct {
	Mode        string `json:"mode"`
	Release     string `json:"release"`
	Strategy    string `json:"strategy,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Status      int    `json:"status,omitempty"`
	Body        string `json:"body,omitempty"`
	Imported    bool   `json:"imported"`
	Error       string `json:"error,omitempty"`
}

func main() {
	if len(os.Args) < 2 {
		fail("usage: keydeliverypeer receive|send [flags]")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:0", "receive: address to listen on")
	to := fs.String("to", "", "send: receiver base URL")
	tlsDir := fs.String("tls-dir", "", "directory holding ca.pem and the node certificates")
	if err := fs.Parse(os.Args[2:]); err != nil {
		fail(err.Error())
	}
	if *tlsDir == "" {
		fail("--tls-dir is required")
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	tb := newPeerTB()
	defer tb.cleanup()

	var res result
	switch os.Args[1] {
	case "receive":
		res = receive(tb, logger, *listen, *tlsDir)
	case "send":
		if *to == "" {
			fail("--to is required")
		}
		res = send(tb, logger, *to, *tlsDir)
	default:
		fail("unknown mode " + os.Args[1])
	}
	res.Release = release.Version()
	emit(res)
}

func receive(tb *peerTB, logger *slog.Logger, listen, tlsDir string) result {
	res := result{Mode: "receive"}
	ctx := context.Background()
	caPool, node, nodePEM, nodeKey := loadTLS(tlsDir, followerNode)

	reg, alloc := newRegistry(tb, logger)
	sealtest.ReserveVolumeTenant(tb, alloc, tenantID)

	dec, err := keydelivery.PrivateKeyFor(keydelivery.KEMP256, nodeKey)
	if err != nil {
		fail("keydelivery.PrivateKeyFor: " + err.Error())
	}
	kd, err := keydelivery.NewReceiver(dec, followerNode)
	if err != nil {
		fail("keydelivery.NewReceiver: " + err.Error())
	}
	recv, err := clusterkeys.NewReceiver(kd, reg, staticPlacement{p: deliveryPlacement()},
		trustDomain, followerNode, caPool, nil, nodePEM, logger)
	if err != nil {
		fail("clusterkeys.NewReceiver: " + err.Error())
	}

	done := make(chan result, 1)
	deliver := recv.Handler()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/cluster/nodekey", recv.NodeKeyHandler())
	mux.HandleFunc("/v1/cluster/keydelivery", func(w http.ResponseWriter, req *http.Request) {
		rec := &recordingWriter{ResponseWriter: w, status: http.StatusOK}
		deliver(rec, req)
		out := result{Mode: "receive", Status: rec.status, Body: strings.TrimSpace(rec.body.String())}
		if tb, err := reg.GetTenant(tenantID); err == nil {
			out.Imported = true
			out.Fingerprint, out.Error = fingerprint(ctx, tb.Barrier())
		}
		select {
		case done <- out:
		default:
		}
	})

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		fail("listen: " + err.Error())
	}
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{node},
			ClientAuth:   tls.VerifyClientCertIfGiven,
			ClientCAs:    caPool,
		},
	}
	go func() { _ = srv.ServeTLS(ln, "", "") }()
	emit(map[string]string{"url": "https://" + ln.Addr().String()})

	select {
	case res = <-done:
	case <-time.After(2 * time.Minute):
		res.Error = "no delivery attempt within 2m"
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	return res
}

func send(tb *peerTB, logger *slog.Logger, to, tlsDir string) result {
	res := result{Mode: "send"}
	ctx := context.Background()
	caPool, node, _, _ := loadTLS(tlsDir, leaderNode)

	reg, alloc := newRegistry(tb, logger)
	sealtest.ReserveVolumeTenant(tb, alloc, tenantID)
	_, tenant, err := reg.RegisterVolumeTenant(ctx, tenantID, true)
	if err != nil {
		fail("RegisterVolumeTenant: " + err.Error())
	}
	if err := tenant.Initialize(ctx, seal.Credentials{}); err != nil {
		fail("Initialize tenant: " + err.Error())
	}
	barrier := tenant.Barrier()
	res.Strategy = string(barrier.PersistedStrategy(ctx))
	if res.Fingerprint, res.Error = fingerprint(ctx, barrier); res.Error != "" {
		return res
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			MinVersion:   tls.VersionTLS13,
			RootCAs:      caPool,
			Certificates: []tls.Certificate{node},
		}},
	}
	sender, err := clusterkeys.NewSender(staticResolver{raftAddr: followerNode + ":9000", url: to},
		client, caPool, trustDomain, nil, logger)
	if err != nil {
		fail("clusterkeys.NewSender: " + err.Error())
	}
	if err := sender.DistributeTenantKey(ctx, deliveryPlacement(), barrier, leaderNode); err != nil {
		res.Error = err.Error()
		return res
	}
	res.Imported = true
	return res
}

// newRegistry builds a barrier registry over a fresh volume pool with an
// initialized system barrier, as each node has.
func newRegistry(tb *peerTB, logger *slog.Logger) (*seal.BarrierRegistry, volume.Allocator) {
	sys, err := seal.NewStorageBarrier(logger, seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "system/root-key"}, seal.NewNoneStrategy())
	if err != nil {
		fail("NewStorageBarrier(system): " + err.Error())
	}
	reg, _, alloc := sealtest.PooledRegistry(tb, sys)
	if err := reg.SystemBarrier().Initialize(context.Background(), seal.Credentials{}); err != nil {
		fail("Initialize system barrier: " + err.Error())
	}
	return reg, alloc
}

// deliveryPlacement is the shard both peers agree on: the sender leads
// replica 1, the receiver holds replica 2.
func deliveryPlacement() *placement.Placement {
	return &placement.Placement{
		TenantID:        tenantID,
		Database:        database,
		ShardID:         1,
		RF:              2,
		LeaderReplicaID: 1,
		Replicas: []placement.Replica{
			{ReplicaID: 1, NodeID: leaderNode, RaftAddr: leaderNode + ":9000"},
			{ReplicaID: 2, NodeID: followerNode, RaftAddr: followerNode + ":9000"},
		},
	}
}

func fingerprint(ctx context.Context, b *seal.StorageBarrier) (string, string) {
	blob, err := b.ExportSealedBlob(ctx)
	if err != nil {
		return "", "ExportSealedBlob: " + err.Error()
	}
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:]), ""
}

func loadTLS(dir, nodeID string) (*x509.CertPool, tls.Certificate, []byte, *ecdsa.PrivateKey) {
	caPEM, err := os.ReadFile(filepath.Clean(filepath.Join(dir, "ca.pem")))
	if err != nil {
		fail(err.Error())
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		fail("no certificate in ca.pem")
	}
	certPEM, err := os.ReadFile(filepath.Clean(filepath.Join(dir, nodeID+".pem")))
	if err != nil {
		fail(err.Error())
	}
	keyPEM, err := os.ReadFile(filepath.Clean(filepath.Join(dir, nodeID+"-key.pem")))
	if err != nil {
		fail(err.Error())
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		fail("X509KeyPair: " + err.Error())
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		fail("no PEM block in " + nodeID + "-key.pem")
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		fail("ParseECPrivateKey: " + err.Error())
	}
	return pool, pair, certPEM, key
}

type staticPlacement struct{ p *placement.Placement }

func (s staticPlacement) Get(context.Context, string, string) (*placement.Placement, error) {
	return s.p, nil
}

type staticResolver struct{ raftAddr, url string }

func (s staticResolver) ResolveAdminURL(raftAddr string) string {
	if raftAddr == s.raftAddr {
		return s.url
	}
	return ""
}

// recordingWriter keeps the status and body the receiver answered.
type recordingWriter struct {
	http.ResponseWriter
	status int
	body   strings.Builder
}

func (r *recordingWriter) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recordingWriter) Write(p []byte) (int, error) {
	r.body.Write(p)
	return r.ResponseWriter.Write(p)
}

// peerTB satisfies the testing.TB that pkg/seal/sealtest's volume-pool
// helpers take, so this program builds its registries exactly as the
// clusterkeys package tests do. Only the methods sealtest calls are
// implemented; the embedded nil TB makes any other call panic loudly.
type peerTB struct {
	testing.TB
	dirs     []string
	cleanups []func()
}

func newPeerTB() *peerTB { return &peerTB{} }

func (p *peerTB) Helper() {}

func (p *peerTB) Fatalf(format string, args ...any) { fail(fmt.Sprintf(format, args...)) }

func (p *peerTB) Cleanup(f func()) { p.cleanups = append(p.cleanups, f) }

func (p *peerTB) TempDir() string {
	dir, err := os.MkdirTemp("", "keydeliverypeer-*")
	if err != nil {
		fail("MkdirTemp: " + err.Error())
	}
	p.dirs = append(p.dirs, dir)
	return dir
}

func (p *peerTB) cleanup() {
	for i := len(p.cleanups) - 1; i >= 0; i-- {
		p.cleanups[i]()
	}
	for _, d := range p.dirs {
		_ = os.RemoveAll(d)
	}
}

func emit(v any) {
	if err := json.NewEncoder(os.Stdout).Encode(v); err != nil {
		fail(err.Error())
	}
}

func fail(msg string) {
	_ = json.NewEncoder(os.Stdout).Encode(result{Mode: "fatal", Release: release.Version(), Error: msg})
	os.Exit(2)
}
