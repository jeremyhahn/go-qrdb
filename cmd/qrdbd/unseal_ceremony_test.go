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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/internal/netport"
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
// /v1/ceremony/initialize against the sealed-window server -- "qrdb init" is
// the only system bootstrap (go-qrdb#548), and it brings the system barrier
// up itself in the same call rather than through the now-refused
// /v1/barrier/initialize. This daemon has auth disabled, but the seal
// subsystem -- and therefore the ceremony service -- is wired whenever
// seal.enabled is true regardless of auth.enabled, so the sealed-window
// listener still answers it. The test supplies the TLS material itself, so
// the client verifies the daemon's certificate against a CA it owns rather
// than skipping verification — a test that turns verification off proves
// nothing about a server that is meant to present a valid certificate.
//
// Readiness is taken from barrierUnsealed rather than from the admin port,
// because the port cannot distinguish the two servers: the sealed-window
// listener and the post-unseal server bind the SAME address, so a TCP dial is
// satisfied while the barrier is still shut.
func TestRunStart_UnsealCeremony_ProceedsToFullStartup(t *testing.T) {
	dir := bootThroughUnsealCeremony(t, func(f *startFlags) { f.enableGRPC = false }, nil)

	// The cluster CA key (Key A) is confirmed persisted into the replicated
	// store during this same boot (confirmClusterCAPersisted, called once
	// waitForLeaderElection succeeds) and the local ca-key.pem fixture --
	// present on disk before runStart ever ran, exactly as a real first
	// node's bootstrap-generated file would be -- is deleted as soon as that
	// is confirmed. No CA private key file survives a completed boot.
	if _, err := os.Stat(filepath.Join(dir, "ca-key.pem")); !os.IsNotExist(err) {
		t.Fatalf("ca-key.pem still exists in the data directory after a completed boot (stat err = %v)", err)
	}
}

// TestRunStart_UnsealCeremony_HandoverKeepsItsPorts is the regression test
// for a node that unsealed and then could not serve: "rest: server failed to
// start on 127.0.0.1:46273: bind: address already in use". The sealed-window
// listener used to close its sockets on unseal and the node's own server
// bound the same ports again only after initializeNode and first boot, so for
// that whole stretch the ports belonged to nobody, and any socket on the host
// could take one -- an outbound connection drawing it as its ephemeral source
// port is enough, and on a loaded CI runner one did.
//
// barrierUnsealed runs in exactly that stretch: after the sealed window has
// stopped serving, before the regular server starts. The probe tries to bind
// every port the handover has to keep there, and also while the sealed window
// serves and once the regular server serves, so the port is shown held at
// every step of the boot rather than at one.
func TestRunStart_UnsealCeremony_HandoverKeepsItsPorts(t *testing.T) {
	thief := newPortThief(t)
	bootThroughUnsealCeremony(t, nil, func(step string, f *startFlags) {
		admin := net.JoinHostPort(f.host, strconv.Itoa(f.adminPort))
		thief.try(step, netport.NetworkTCP, admin)
		thief.try(step, netport.NetworkUDP, admin)
		thief.try(step, netport.NetworkTCP, net.JoinHostPort(f.host, strconv.Itoa(f.grpcPort)))
	})
}

// TestRunStart_UnsealCeremony_HandoverKeepsTheQUICPort is
// TestRunStart_UnsealCeremony_HandoverKeepsItsPorts for the native QUIC
// transport's UDP port: no port is released while the node is sealed, so
// this one is handed over too, not closed by the sealed window and bound
// again by the regular server.
func TestRunStart_UnsealCeremony_HandoverKeepsTheQUICPort(t *testing.T) {
	thief := newPortThief(t)
	bootThroughUnsealCeremony(t, func(f *startFlags) {
		f.enableQUIC = true
		f.quicPort = drawTestPort(t, netport.NetworkUDP)
	}, func(step string, f *startFlags) {
		thief.try(step, netport.NetworkUDP, net.JoinHostPort(f.host, strconv.Itoa(f.quicPort)))
	})
}

// TestRunStart_UnsealCeremony_HandoverKeepsTheMCPPort is
// TestRunStart_UnsealCeremony_HandoverKeepsItsPorts for the MCP server's TCP
// port.
func TestRunStart_UnsealCeremony_HandoverKeepsTheMCPPort(t *testing.T) {
	thief := newPortThief(t)
	bootThroughUnsealCeremony(t, func(f *startFlags) {
		f.enableMCP = true
		f.mcpPort = drawTestPort(t, netport.NetworkTCP)
	}, func(step string, f *startFlags) {
		thief.try(step, netport.NetworkTCP, net.JoinHostPort(f.host, strconv.Itoa(f.mcpPort)))
	})
}

// The steps of a sealed boot at which bootThroughUnsealCeremony runs its
// probe, worded to complete "the port was free ...".
const (
	stepSealed   = "while the sealed window served"
	stepUnsealed = "between the sealed window and the regular server"
	stepServing  = "while the regular server served"
)

// portThief binds, at a boot step, a port the daemon should be holding, and
// keeps whatever it gets until the test ends, so a port that was released
// fails the boot the way it failed in CI rather than merely being recorded.
// startDaemon starts again on fresh ports when a boot loses one, so the
// record spans every attempt and is reported from a cleanup, which runs even
// when the last attempt ends the test.
type portThief struct {
	mu     sync.Mutex
	stolen []string
	held   []io.Closer
}

// newPortThief returns a portThief whose findings fail t and whose sockets
// are closed when t ends.
func newPortThief(t *testing.T) *portThief {
	t.Helper()
	p := &portThief{}
	t.Cleanup(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, c := range p.held {
			_ = c.Close()
		}
	})
	t.Cleanup(func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, s := range p.stolen {
			t.Errorf("%s: the handover released the port instead of handing it over", s)
		}
	})
	return p
}

// try binds addr on network and records step when the bind succeeds.
func (p *portThief) try(step, network, addr string) {
	var (
		c   io.Closer
		err error
	)
	if network == netport.NetworkUDP {
		c, err = net.ListenPacket(network, addr)
	} else {
		c, err = net.Listen(network, addr)
	}
	if err != nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.held = append(p.held, c)
	p.stolen = append(p.stolen, network+" "+addr+" was free "+step)
}

// drawTestPort returns a port that is free on testBindHost for network.
func drawTestPort(t *testing.T, network string) int {
	t.Helper()
	port, err := netport.FreePortOn(testBindHost, network)
	if err != nil {
		t.Fatalf("%s port: %v", network, err)
	}
	return port
}

// caPool returns a pool holding bundle's CA certificate, the one root a client
// of this test's daemon trusts.
func caPool(t *testing.T, bundle *TLSBundle) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(bundle.CACertPEM) {
		t.Fatal("the test's own CA PEM did not parse")
	}
	return pool
}

// sealedWindowServing reports whether the daemon's own HTTPS server answers on
// port, by a TLS handshake that verifies against roots.
//
// A TCP dial is not that evidence, and taking it for that is how this test
// stole its own port (run 69190). bindServerSockets listens on the REST port
// first and binds the gRPC, QUIC and MCP sockets after it, and the kernel
// completes a connection to a listening socket before any server runs, so a
// dial can succeed while the later ports are not yet bound -- and a probe at
// that moment takes one before the daemon does. A handshake needs the
// server's accept loop, which starts only after every socket is bound, and a
// certificate this attempt's CA signed, which no other process on the host
// presents.
func sealedWindowServing(roots *x509.CertPool, port int) func() bool {
	addr := net.JoinHostPort(testBindHost, strconv.Itoa(port))
	cfg := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	return func() bool {
		conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 500 * time.Millisecond}, "tcp", addr, cfg)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}
}

// serveTLSFixture serves bundle's leaf certificate on a fresh testBindHost
// port until t ends and returns the port. Every connection is handshaken and
// closed.
func serveTLSFixture(t *testing.T, bundle *TLSBundle) int {
	t.Helper()
	cert, err := tls.X509KeyPair(bundle.CertPEM, bundle.KeyPEM)
	if err != nil {
		t.Fatalf("leaf key pair: %v", err)
	}
	raw, err := net.Listen(netport.NetworkTCP, net.JoinHostPort(testBindHost, "0"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ln := tls.NewListener(raw, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.(*tls.Conn).Handshake()
			_ = c.Close()
		}
	}()
	return raw.Addr().(*net.TCPAddr).Port
}

// A socket that accepts at the kernel but has no server behind it is the
// state bindServerSockets leaves between its REST bind and its later ones:
// it is not the sealed window serving.
func TestSealedWindowServing_ListeningSocketIsNotServing(t *testing.T) {
	bundle := writeTLSFixtures(t, t.TempDir())
	ln, err := net.Listen(netport.NetworkTCP, net.JoinHostPort(testBindHost, "0"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	if sealedWindowServing(caPool(t, bundle), ln.Addr().(*net.TCPAddr).Port)() {
		t.Fatal("a listening socket nobody serves was taken for the sealed window serving")
	}
}

func TestSealedWindowServing_OwnServerIsServing(t *testing.T) {
	bundle := writeTLSFixtures(t, t.TempDir())
	port := serveTLSFixture(t, bundle)

	if !sealedWindowServing(caPool(t, bundle), port)() {
		t.Fatal("a server presenting the attempt's own certificate was not taken for serving")
	}
}

// A server another CA signed is some other process on the port, not this
// attempt's daemon.
func TestSealedWindowServing_ForeignServerIsNotServing(t *testing.T) {
	port := serveTLSFixture(t, writeTLSFixtures(t, t.TempDir()))
	own := writeTLSFixtures(t, t.TempDir())

	if sealedWindowServing(caPool(t, own), port)() {
		t.Fatal("a server another CA signed was taken for this attempt's daemon")
	}
}

// bootThroughUnsealCeremony boots runStart with a sealed barrier, performs the
// unseal ceremony through the sealed-window listener, waits for the regular
// server to serve, shuts the daemon down cleanly, and returns the data
// directory of the attempt that served. configure, when non-nil, adjusts each
// attempt's flags before runStart. probe, when non-nil, runs with the
// attempt's flags at each step: once the sealed window serves (stepSealed),
// on runStart's goroutine between the sealed window's teardown and the
// regular server's start (stepUnsealed), and once the regular server serves
// (stepServing).
func bootThroughUnsealCeremony(t *testing.T, configure func(f *startFlags), probe func(step string, f *startFlags)) string {
	t.Helper()
	if probe == nil {
		probe = func(string, *startFlags) {}
	}

	// TWO milestones, because they are different claims. barrierUnsealed says
	// the ceremony worked; serverServing says the handover completed. Between
	// them lie initializeNode, first boot, service construction and the server
	// bind, any of which can fail — so a test that stopped at the first would
	// prove the barrier opened and nothing about reaching a serving node,
	// while being named for the latter.
	//
	// f is the current attempt's flags: startDaemon assigns it before it runs
	// runStart, which is the goroutine that calls barrierUnsealed.
	var f *startFlags
	unsealed := make(chan struct{}, 1)
	origUnsealed := barrierUnsealed
	t.Cleanup(func() { barrierUnsealed = origUnsealed })
	barrierUnsealed = func() {
		probe(stepUnsealed, f)
		select {
		case unsealed <- struct{}{}:
		default:
		}
	}
	serving := servingHook(t)

	// The sealed-window listener accepting is the cue that the ceremony can be
	// performed -- it is NOT evidence of full startup, which is the confusion
	// this test exists to remove. So the attempt is ready only once the
	// ceremony has been accepted AND the regular server is serving: the
	// Noise bootstrap listener binds after the unseal, and a port another
	// process takes there ends the attempt as surely as one taken before
	// the sealed window, so startDaemon must still be able to start again.
	//
	// From here to the end of the test every exit is a t.Fatalf, and a Fatalf
	// ends this goroutine without reaching the shutdown below. startDaemon
	// registers the guard that stops the daemon on those paths -- the
	// initializeCeremony failure, the readiness timeout, and a runStart that
	// exits early -- so that a regression reports as this test failing rather
	// than as a daemon holding its ports and the process signal handlers
	// through everything that runs after it.
	var (
		dir         string
		wasUnsealed func() bool
	)
	done := startDaemon(t, func(p daemonPorts) daemonLaunch {
		dir = t.TempDir()
		// The daemon serves these, and the client below verifies against them.
		bundle := writeTLSFixtures(t, dir)

		// "pebble", the production default. This test exercises the
		// unseal-to-serving handover, which does not depend on which --sm
		// backs it.
		f = fullStartFlags(dir, p)
		f.sealConfig = qrdbconfig.SealConfig{
			// Enabled is the gate: without it runStart never brings up the
			// sealed window and the admin port is the ordinary server, which
			// is how the first draft of this test 404ed on the ceremony.
			Enabled:     true,
			Strategy:    "",
			RootKeyPath: "__sys:seal/root_key",
		}
		if configure != nil {
			configure(f)
		}

		// The sealed window is serving once it completes a handshake, not
		// once the admin port accepts a connection: see sealedWindowServing.
		listening, isServing := sealedWindowServing(caPool(t, bundle), p.admin), hookFired(serving)
		wasUnsealed = hookFired(unsealed)
		initialized := false
		return runStartLaunch(t, f, func() bool {
			if !initialized {
				if !listening() {
					return false
				}
				probe(stepSealed, f)
				initializeCeremony(t, bundle, p.admin, "unseal-ceremony-passphrase")
				initialized = true
			}
			return isServing()
		})
	})

	// Serving is the second milestone; the first must have come before it,
	// or the node served without the handover this test is named for.
	if !wasUnsealed() {
		t.Fatal("the regular server is serving but the barrier never " +
			"reported the unseal: startup did not pass through awaitUnseal")
	}
	probe(stepServing, f)

	if runErr := signalUntilReturn(t, done, syscall.SIGTERM); runErr != nil {
		t.Fatalf("runStart returned an error after a clean shutdown: %v", runErr)
	}

	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
	return dir
}

// initializeCeremony performs the operator's half of the ceremony: a single
// POST to /v1/ceremony/initialize that both brings the system barrier up
// (secret, the passphrase strategy's registered strategy expects) and runs
// the bootstrap-admin ceremony, exactly as "qrdb init" does (go-qrdb#548).
// This test's daemon has auth disabled and never looks at the admin
// credential the ceremony issues, so so_pin/user_pin are placeholders and
// admin_generate selects the dev-only generate path explicitly -- there is
// no credential-less default (go-qrdb#548 review) -- and its output is
// discarded.
func initializeCeremony(t *testing.T, bundle *TLSBundle, adminPort int, secret string) {
	t.Helper()

	pool := caPool(t, bundle)
	client := &http.Client{
		Timeout: readinessTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    pool,
				MinVersion: tls.VersionTLS12,
			},
		},
	}

	body, err := json.Marshal(map[string]any{
		"so_pin":   "unseal-ceremony-so-pin",
		"user_pin": "unseal-ceremony-user-pin",
		// []byte, not the bare string: CeremonyInitializeRequest.Secret is
		// []byte (go-qrdb#560 review item 7), and encoding/json renders a
		// []byte as base64 on marshal -- the wire shape the REST decoder on
		// the other end expects. A bare string marshals as plain JSON
		// text instead, which is not valid base64 and the decoder rejects.
		"secret":         []byte(secret),
		"admin_generate": true,
		// A system SO, always: under FIPS 140-3 separation of duties is
		// strict, which refuses a ceremony without one.
		"so_username": "system-so",
		"so_csr_pem":  systemSOCSRPEM(t),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	url := fmt.Sprintf("https://127.0.0.1:%d/v1/ceremony/initialize", adminPort)

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
	t.Fatalf("ceremony initialize never succeeded within %v: %v", readinessTimeout, lastErr)
}

// systemSOCSRPEM returns a PEM CSR for the system SO over a fresh P-256 key,
// a key that is never the admin's.
func systemSOCSRPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("systemSOCSRPEM: generate key: %v", err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "system-so"}}, key)
	if err != nil {
		t.Fatalf("systemSOCSRPEM: create CSR: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}
