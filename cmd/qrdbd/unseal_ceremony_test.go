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

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"syscall"
	"testing"
	"time"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
)

// A sealed daemon is carried through the unseal ceremony to full startup.
//
// This is the one startup path a production node with seal enabled takes on
// every boot, and until now no test covered the HANDOVER: one test exercised
// the sealed window and stopped, another exercised first boot with seal off.
// Neither covered the transition between them, which is where a regression is
// least visible — the daemon comes up, serves the sealed window, and simply
// never proceeds (go-qrdb#96).
//
// The ceremony is driven the way an operator drives it: an HTTPS POST to
// /v1/barrier/initialize against the sealed-window server. The test supplies
// the TLS material itself, so the client verifies the daemon's certificate
// against a CA it owns rather than skipping verification — a test that turns
// verification off proves nothing about a server that is meant to present a
// valid certificate.
//
// Readiness is taken from barrierUnsealed rather than from the admin port,
// because the port cannot distinguish the two servers: the sealed-window
// listener and the post-unseal server bind the SAME address, so a TCP dial is
// satisfied while the barrier is still shut.
func TestRunStart_UnsealCeremony_ProceedsToFullStartup(t *testing.T) {
	dir := t.TempDir()

	// The daemon serves these, and the client below verifies against them.
	bundle := writeTLSFixtures(t, dir)

	adminPort, err := freePortOn(testBindHost, networkTCP, networkUDP)
	if err != nil {
		t.Fatalf("admin port: %v", err)
	}
	raftPort, err := freePortOn(raftListenHost, networkUDP)
	if err != nil {
		t.Fatalf("raft port: %v", err)
	}
	grpcPort, err := freePortOn(testBindHost, networkTCP)
	if err != nil {
		t.Fatalf("grpc port: %v", err)
	}
	raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPort)

	// TWO milestones, because they are different claims. barrierUnsealed says
	// the ceremony worked; serverServing says the handover completed. Between
	// them lie initializeNode, first boot, service construction and the server
	// bind, any of which can fail — so a test that stopped at the first would
	// prove the barrier opened and nothing about reaching a serving node,
	// while being named for the latter.
	unsealed := make(chan struct{}, 1)
	origUnsealed := barrierUnsealed
	t.Cleanup(func() { barrierUnsealed = origUnsealed })
	barrierUnsealed = func() {
		select {
		case unsealed <- struct{}{}:
		default:
		}
	}

	serving := make(chan struct{}, 1)
	origServing := serverServing
	t.Cleanup(func() { serverServing = origServing })
	serverServing = func() {
		select {
		case serving <- struct{}{}:
		default:
		}
	}

	f := &startFlags{
		dataDir: dir,
		// "pebble", the production default. This test exercises the
		// unseal-to-serving handover, which does not depend on which --sm
		// backs it.
		smType:          "pebble",
		discoveryMethod: "static",
		peers:           []string{fmt.Sprintf("1=%s", raftAddr)},
		listen:          fmt.Sprintf("0.0.0.0:%d", raftPort),
		raftAddress:     raftAddr,
		shardID:         1,
		replicaID:       1,
		deploymentID:    1,
		adminPort:       adminPort,
		grpcPort:        grpcPort,
		authEnabled:     false,
		enableREST:      true,
		sealConfig: qrdbconfig.SealConfig{
			// Enabled is the gate: without it runStart never brings up the
			// sealed window and the admin port is the ordinary server, which
			// is how the first draft of this test 404ed on the ceremony.
			Enabled:     true,
			Strategy:    "",
			RootKeyPath: "__sys:seal/root_key",
		},
		host: testBindHost,
	}

	done := make(chan error, 1)
	go func() { done <- runStart(f) }()

	// From here to the end of the test every exit is a t.Fatalf, and a Fatalf
	// ends this goroutine without reaching the shutdown below. The guard is
	// what stops the daemon on those paths -- the initializeBarrier failure,
	// both milestone timeouts, and a runStart that exits early -- so that a
	// regression reports as this test failing rather than as a daemon holding
	// its ports and the process signal handlers through everything that runs
	// after it.
	//
	// awaitReady registers the same guard, which is why the failure paths were
	// already covered before this line existed. It is stated here anyway,
	// because a guard installed as a side effect of a readiness helper is not
	// visible where the daemon is created, and the invariant belongs next to
	// the goroutine it constrains. Registration is deduplicated per daemon, so
	// the two calls install one cleanup.
	guardDaemonShutdown(t, done)

	// The sealed-window listener accepting is the cue that the ceremony can be
	// performed -- it is NOT evidence of full startup, which is the confusion
	// this test exists to remove.
	awaitReady(t, done, listenerReady(fmt.Sprintf("127.0.0.1:%d", adminPort)), readyDeadline(t))

	initializeBarrier(t, bundle, adminPort, "unseal-ceremony-passphrase")

	select {
	case <-unsealed:
	case runErr := <-done:
		t.Fatalf("runStart exited during the ceremony instead of unsealing: %v", runErr)
	case <-time.After(readinessTimeout):
		t.Fatalf("the barrier was initialized but startup did not proceed past "+
			"awaitUnseal within %v: the daemon is serving its sealed window and "+
			"going no further", readinessTimeout)
	}

	// The unseal is not the destination. Everything after it has to succeed
	// for the node to be serving, and that is what this test is named for.
	select {
	case <-serving:
	case runErr := <-done:
		t.Fatalf("runStart exited after unsealing but before serving: the "+
			"handover from the sealed window to the regular server failed: %v", runErr)
	case <-time.After(readinessTimeout):
		t.Fatalf("the barrier unsealed but the regular server was not serving "+
			"within %v: startup stalled somewhere between awaitUnseal and "+
			"srv.Start — initializeNode, first boot, or the server bind",
			readinessTimeout)
	}

	if runErr := signalUntilReturn(t, done, syscall.SIGTERM); runErr != nil {
		t.Fatalf("runStart returned an error after a clean shutdown: %v", runErr)
	}

	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
}

// initializeBarrier performs the operator's half of the ceremony.
func initializeBarrier(t *testing.T, bundle *TLSBundle, adminPort int, secret string) {
	t.Helper()

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(bundle.CACertPEM) {
		t.Fatal("the test's own CA PEM did not parse")
	}

	client := &http.Client{
		Timeout: readinessTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    pool,
				MinVersion: tls.VersionTLS12,
			},
		},
	}

	// The wire field is []byte so the daemon can zeroize it, and
	// encoding/json renders a []byte as base64. Marshaling the plaintext as a
	// string would be accepted by the encoder and REJECTED by the server.
	body, err := json.Marshal(map[string]any{"secret": []byte(secret)})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	url := fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/initialize", adminPort)

	// The listener accepts before the handler is necessarily wired, so this
	// retries until the endpoint answers rather than betting on one attempt.
	deadline := time.Now().Add(readinessTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		req, rerr := http.NewRequestWithContext(context.Background(),
			http.MethodPost, url, bytes.NewReader(body))
		if rerr != nil {
			t.Fatalf("new request: %v", rerr)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, derr := client.Do(req)
		if derr != nil {
			lastErr = derr
			time.Sleep(signalRetryInterval)
			continue
		}
		_ = resp.Body.Close()

		if resp.StatusCode < 300 {
			return
		}
		lastErr = fmt.Errorf("status %d", resp.StatusCode)
		time.Sleep(signalRetryInterval)
	}
	t.Fatalf("barrier initialize never succeeded within %v: %v", readinessTimeout, lastErr)
}
