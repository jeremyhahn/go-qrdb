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

//go:build benchmark

// Command leasekeepalive measures the round-trip latency of
// LeaseKeepAlive against a 3-node QRDB Raft cluster: this is the
// regression guard for go-qrdb#275, whose tail came from a second,
// separately linearized read issued after the proposal that reset a
// lease's TTL.
//
// It starts three qrdbd nodes with static discovery and TLS (auth
// disabled, matching test/performance/qrdb's existing cluster benchmarks),
// waits for a leader, grants one lease and times a sequential run of
// LeaseKeepAlive calls against it over gRPC. Results are printed as a
// Markdown table and written as JSON.
//
//	make bench-leasekeepalive
//	go run -tags benchmark ./test/performance/leasekeepalive
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	qrdbcluster "github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

var (
	workDir = flag.String("work", "", "directory for certificates and data (default: a fresh temporary directory)")
	ops     = flag.Int("ops", 500, "KeepAlive calls timed")
	label   = flag.String("label", "run", "label recorded on the result")
	outFile = flag.String("out", "", "JSON results file (default: <label>-leasekeepalive.json in the work directory)")
	writers = flag.Int("writers", 8, "concurrent background Put writers run for the duration of the measurement, so the leader's read lease is under the same churn #139 measured")
)

// result is the KeepAlive latency distribution for one run.
type result struct {
	Label string  `json:"label"`
	N     int     `json:"n"`
	P50ms float64 `json:"p50_ms"`
	P90ms float64 `json:"p90_ms"`
	P99ms float64 `json:"p99_ms"`
	MaxMs float64 `json:"max_ms"`
}

// benchError is a step of the run that failed.
type benchError struct {
	Step string
	Err  error
}

func (e *benchError) Error() string { return "leasekeepalive: " + e.Step + ": " + e.Err.Error() }
func (e *benchError) Unwrap() error { return e.Err }

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	dir := *workDir
	if dir == "" {
		d, err := os.MkdirTemp("", "qrdb-leasekeepalive-*")
		if err != nil {
			return &benchError{Step: "work dir", Err: err}
		}
		dir = d
	}

	cluster, err := startCluster(dir)
	if err != nil {
		return err
	}
	defer cluster.stop()

	lat, err := cluster.measureKeepAlive(*ops)
	if err != nil {
		return err
	}
	r := summarize(lat)

	// The results file is written outside dir: startCluster's cleanup removes
	// dir's node certificates and data wholesale, and dir is the only
	// candidate location when -work is not given.
	out := *outFile
	if out == "" {
		out = *label + "-leasekeepalive.json"
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return &benchError{Step: "encode results", Err: err}
	}
	if err := os.WriteFile(out, b, 0o600); err != nil {
		return &benchError{Step: "write results", Err: err}
	}

	fmt.Printf("| %s | %d | %.3f | %.3f | %.3f | %.3f |\n", r.Label, r.N, r.P50ms, r.P90ms, r.P99ms, r.MaxMs)
	fmt.Println("results:", out)
	return nil
}

// cluster is a running 3-node static-discovery qrdbd cluster with a gRPC
// client connected to its leader.
type cluster struct {
	daemons    []*qrdbdharness.Daemon
	dataDir    string
	conn       *grpc.ClientConn
	grpcClient pb.QRDBServiceClient
}

func (c *cluster) stop() {
	for _, d := range c.daemons {
		_ = d.Stop()
	}
	if c.conn != nil {
		_ = c.conn.Close()
	}
	_ = os.RemoveAll(c.dataDir)
}

// startCluster launches three qrdbd nodes with static discovery, TLS and
// auth disabled -- the same shape test/performance/qrdb's SetupQRDBCluster
// uses for its multi-node benchmarks -- and returns once a leader is
// elected and a gRPC client is connected to it.
func startCluster(dir string) (*cluster, error) {
	const nodes = 3
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, &benchError{Step: "work dir", Err: err}
	}

	binary, err := qrdbdharness.BuildBinary(qrdbdharness.DaemonPackage)
	if err != nil {
		return nil, &benchError{Step: "build qrdbd", Err: err}
	}
	ca, err := qrdbdharness.NewCA(dir)
	if err != nil {
		return nil, &benchError{Step: "CA", Err: err}
	}

	raftPorts := make([]int, nodes)
	adminPorts := make([]int, nodes)
	grpcPorts := make([]int, nodes)
	for i := range nodes {
		p, err := qrdbdharness.ReserveUDPPort()
		if err != nil {
			return nil, &benchError{Step: "raft port", Err: err}
		}
		raftPorts[i] = p
		ports, err := qrdbdharness.ReserveTCPPorts(2)
		if err != nil {
			return nil, &benchError{Step: "tcp ports", Err: err}
		}
		adminPorts[i] = ports[0]
		grpcPorts[i] = ports[1]
	}

	peers := ""
	for i := range nodes {
		if i > 0 {
			peers += ","
		}
		peers += fmt.Sprintf("%d=127.0.0.1:%d", i+1, raftPorts[i])
	}

	// Every node needs every peer's Raft member identity before any of them
	// starts (go-quicraft admits a member by this identity alone, #556), so
	// certificates are issued up front.
	nodeCerts := make([]*qrdbdharness.KeyPair, nodes)
	identityParts := make([]string, nodes)
	for i := range nodes {
		nodeCert, err := ca.Issue(dir, qrdbdharness.WithSubject(fmt.Sprintf("node%d", i+1), "system"), qrdbdharness.WithServerAuth())
		if err != nil {
			return nil, &benchError{Step: "node certificate", Err: err}
		}
		nodeCerts[i] = nodeCert
		identity, err := qrdbcluster.MemberIdentityFromPEM(nodeCert.CertPEM)
		if err != nil {
			return nil, &benchError{Step: "member identity", Err: err}
		}
		identityParts[i] = fmt.Sprintf("%d=%s", i+1, identity)
	}
	memberIdentities := identityParts[0]
	for _, part := range identityParts[1:] {
		memberIdentities += "," + part
	}

	c := &cluster{dataDir: dir}
	for i := range nodes {
		nodeDataDir := filepath.Join(dir, fmt.Sprintf("node%d", i+1))
		raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPorts[i])
		args := []string{"start",
			"--discovery", "static", "--peers", peers,
			"--member-identities", memberIdentities,
			"--replica-id", strconv.Itoa(i + 1),
			"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[i]), "--raft-address", raftAddr,
			"--admin-port", strconv.Itoa(adminPorts[i]), "--grpc-port", strconv.Itoa(grpcPorts[i]),
			"--data-dir", nodeDataDir, "--sm", "pebble",
			"--auth-enabled=false", "--log-level", "warn",
		}
		args = append(args, ca.TLSFlags(nodeCerts[i])...)
		// #nosec G204 -- binary is this run's own build output; args are this
		// run's own flags.
		d, err := qrdbdharness.Start(binary, args, qrdbdharness.WithOutput(io.Discard))
		if err != nil {
			return nil, &benchError{Step: fmt.Sprintf("start node %d", i+1), Err: err}
		}
		c.daemons = append(c.daemons, d)
	}

	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: ca.Pool, MinVersion: tls.VersionTLS13},
		ForceAttemptHTTP2: true,
	}}
	for _, port := range adminPorts {
		if err := waitHealthy(client, port, 30*time.Second); err != nil {
			c.stop()
			return nil, err
		}
	}
	leaderIdx, err := waitLeader(client, adminPorts, 30*time.Second)
	if err != nil {
		c.stop()
		return nil, err
	}

	tlsCfg := &tls.Config{RootCAs: ca.Pool, MinVersion: tls.VersionTLS13, ServerName: "localhost"}
	conn, err := grpc.NewClient(fmt.Sprintf("127.0.0.1:%d", grpcPorts[leaderIdx]), grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		c.stop()
		return nil, &benchError{Step: "grpc dial", Err: err}
	}
	c.conn = conn
	c.grpcClient = pb.NewQRDBServiceClient(conn)
	return c, nil
}

// waitHealthy polls one node's /health endpoint until it answers 200.
func waitHealthy(client *http.Client, port int, timeout time.Duration) error {
	url := fmt.Sprintf("https://127.0.0.1:%d/health", port)
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return &benchError{Step: "wait healthy :" + strconv.Itoa(port), Err: lastErr}
}

// waitLeader polls every node's /status endpoint until one reports
// leadership, returning its index into ports.
func waitLeader(client *http.Client, ports []int, timeout time.Duration) (int, error) {
	type statusResponse struct {
		Shards []struct {
			IsLeader bool `json:"is_leader"`
		} `json:"shards"`
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		for i, port := range ports {
			url := fmt.Sprintf("https://127.0.0.1:%d/status", port)
			resp, err := client.Get(url)
			if err != nil {
				lastErr = err
				continue
			}
			var status statusResponse
			decodeErr := json.NewDecoder(resp.Body).Decode(&status)
			resp.Body.Close()
			if decodeErr != nil {
				lastErr = decodeErr
				continue
			}
			for _, shard := range status.Shards {
				if shard.IsLeader {
					return i, nil
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0, &benchError{Step: "wait leader", Err: lastErr}
}

// measureKeepAlive grants one lease and times n sequential KeepAlive calls
// against it, with background Put traffic running concurrently so the
// leader's read lease churns the way it did under the #139 measurement:
// KeepAlive's eliminated second SyncRead paid a ReadIndex round trip
// whenever that lease was not valid at the moment the call landed.
func (c *cluster) measureKeepAlive(n int) ([]time.Duration, error) {
	grantCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	grantResp, err := c.grpcClient.LeaseGrant(grantCtx, &pb.LeaseGrantRequest{Ttl: 3600})
	cancel()
	if err != nil {
		return nil, &benchError{Step: "lease grant", Err: err}
	}
	leaseID := grantResp.GetLease().GetId()

	stopWriters := c.startBackgroundWriters(*writers)
	defer stopWriters()

	lat := make([]time.Duration, 0, n)
	for range n {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		t0 := time.Now()
		_, err := c.grpcClient.LeaseKeepAlive(ctx, &pb.LeaseKeepAliveRequest{LeaseId: leaseID})
		cancel()
		if err != nil {
			return nil, &benchError{Step: "lease keepalive", Err: err}
		}
		lat = append(lat, time.Since(t0))
	}
	return lat, nil
}

// startBackgroundWriters runs n goroutines issuing Put requests in a tight
// loop until the returned stop function is called, which waits for them to
// drain. It generates continuous Raft commit traffic concurrent with a
// measurement.
func (c *cluster) startBackgroundWriters(n int) func() {
	stop := make(chan struct{})
	done := make(chan struct{}, n)
	for i := range n {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			key := fmt.Sprintf("leasekeepalive-writer-%d", i)
			value := []byte("x")
			for {
				select {
				case <-stop:
					return
				default:
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, _ = c.grpcClient.Put(ctx, &pb.PutRequest{Key: key, Value: value})
				cancel()
			}
		}(i)
	}
	return func() {
		close(stop)
		for range n {
			<-done
		}
	}
}

// summarize reduces the measured latencies to their percentiles.
func summarize(lat []time.Duration) result {
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	pct := func(p float64) float64 {
		if len(lat) == 0 {
			return 0
		}
		return float64(lat[int(float64(len(lat)-1)*p)].Microseconds()) / 1000
	}
	return result{Label: *label, N: len(lat), P50ms: pct(0.50), P90ms: pct(0.90), P99ms: pct(0.99), MaxMs: pct(1)}
}
