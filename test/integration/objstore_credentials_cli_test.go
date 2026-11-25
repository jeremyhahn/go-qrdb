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

//go:build integration

package integration_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Fake object-storage credential material: never a real key.
const (
	fakeObjstoreKey    = "FAKEACCESSKEYID0000"
	fakeObjstoreSecret = "fake-objstore-secret-for-integration"
)

// writeObjstoreCredentialFile writes a fake credential to a file with mode.
func writeObjstoreCredentialFile(t *testing.T, dir, name, value string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(value+"\n"), mode); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", name, err)
	}
	return path
}

// writeObjstoreModeConfig writes a standalone configuration whose node store
// (storage.backend) is the objstore backend on the local provider; objstore is the
// YAML appended under storage.objstore after its settings.
func writeObjstoreModeConfig(t *testing.T, objstore string) string {
	t.Helper()
	dir := t.TempDir()
	body := fmt.Sprintf("seal:\n  enabled: true\n  strategy: passphrase\n"+
		"storage:\n  backend: \"objstore\"\n  objstore:\n    provider: local\n    settings:\n      path: %q\n%s",
		filepath.Join(dir, "bucket"), objstore)
	path := filepath.Join(dir, "qrdb.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestObjstoreCredentials_RefusesAnInlineCredential proves qrdbd start
// refuses a provider credential written into the configuration file, under
// settings or under credentials, naming the key and never echoing the value.
func TestObjstoreCredentials_RefusesAnInlineCredential(t *testing.T) {
	bin := buildBinary(t)
	cases := map[string]struct {
		objstore string
		names    string
	}{
		"in settings": {
			objstore: "      secret_key: " + fakeObjstoreSecret + "\n",
			names:    "storage.objstore.settings",
		},
		"under credentials": {
			objstore: "    credentials:\n      secret: " + fakeObjstoreSecret + "\n",
			names:    "storage.objstore.credentials.secret",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out := &watchOutputBuf{}
			cmd := spawnNode(t, bin, []string{
				"start",
				"--config", writeObjstoreModeConfig(t, tc.objstore),
				"--mode", "standalone",
				"--data-dir", t.TempDir(),
			}, out)
			if err := awaitNodeExit(t, cmd, 30*time.Second); err == nil {
				t.Fatalf("qrdbd start accepted an inline credential:\n%s", out)
			}
			if strings.Contains(out.String(), fakeObjstoreSecret) {
				t.Fatal("qrdbd start echoed the credential")
			}
			if !strings.Contains(out.String(), tc.names) {
				t.Fatalf("output does not name %q:\n%s", tc.names, out)
			}
		})
	}
}

// TestObjstoreCredentials_IgnoredCredentialFailsTheBoot proves a credential
// reference for a provider that does not read it stops the boot once the
// barrier unseals rather than open the store under the ambient identity: the
// "local" provider reads no credential, the error names the refused file, and
// the secret never reaches the output.
func TestObjstoreCredentials_IgnoredCredentialFailsTheBoot(t *testing.T) {
	bin := buildBinary(t)
	ca := generateTestCA(t)
	secrets := t.TempDir()
	keyFile := writeObjstoreCredentialFile(t, secrets, "access-key", fakeObjstoreKey, 0o600)
	secretFile := writeObjstoreCredentialFile(t, secrets, "secret-key", fakeObjstoreSecret, 0o600)
	cfg := writeObjstoreModeConfig(t, fmt.Sprintf(
		"    credentials:\n      key_file: %q\n      secret_file: %q\n", keyFile, secretFile))

	restPort := allocateTCPPorts(t, 1)[0]
	args := append([]string{
		"start",
		"--config", cfg,
		"--mode", "standalone",
		"--data-dir", t.TempDir(),
		"--admin-port", strconv.Itoa(restPort),
		"--auth-enabled",
		"--enable-rest",
	}, ca.tlsFlags(t)...)
	out := &watchOutputBuf{}
	cmd := spawnNode(t, bin, args, out)

	waitForBarrierStatusReachable(t, ca, restPort, 90*time.Second)
	initAndUnsealBarrier(t, ca, restPort, "objstore-credentials-passphrase")

	if err := awaitNodeExit(t, cmd, 60*time.Second); err == nil {
		t.Fatalf("qrdbd booted with a credential the local provider ignores:\n%s", out)
	}
	if !strings.Contains(out.String(), `provider "local" does not read the access key credential`) ||
		!strings.Contains(out.String(), strconv.Quote(keyFile)) {
		t.Fatalf("output does not refuse the key file %q as ignored:\n%s", keyFile, out)
	}
	if strings.Contains(out.String(), fakeObjstoreSecret) {
		t.Fatal("qrdbd echoed the credential")
	}
}

// standaloneBackendSwitchNode is a standalone qrdbd whose configuration file
// can be rewritten to name another backend before a restart on the same data
// directory and seal store.
type standaloneBackendSwitchNode struct {
	*multiProtoNode
	bin     string
	args    []string
	cfgPath string
	proc    *exec.Cmd

	// adminOnDataPlaneLost is set while the node runs the memory backend,
	// whose data plane (and the admin binding on it) a restart discards.
	adminOnDataPlaneLost bool
}

// writeStandaloneBackendConfig writes the standalone configuration naming
// backend as the default database's; objstoreYAML is appended verbatim.
func writeStandaloneBackendConfig(t *testing.T, path, backend, objstoreYAML string) {
	t.Helper()
	body := fmt.Sprintf("seal:\n  enabled: true\n  strategy: passphrase\n"+
		"databases:\n  - name: \"default\"\n    backend: %q\n%s", backend, objstoreYAML)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// startStandaloneBackendSwitchNode boots a standalone node on backend with
// every transport enabled, initializes and unseals its barrier, and waits
// until each transport serves the CLI.
func startStandaloneBackendSwitchNode(t *testing.T, backend, objstoreYAML string) *standaloneBackendSwitchNode {
	t.Helper()
	bin := buildBinary(t)
	cli := buildCLIBinary(t)
	ca := generateTestCA(t)
	dataDir := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "qrdbd.yaml")
	writeStandaloneBackendConfig(t, cfgPath, backend, objstoreYAML)

	sockDir, err := os.MkdirTemp("", "qrdb-bs-")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	unixSocket := filepath.Join(sockDir, "qrdbd.sock")

	quicPort := allocatePorts(t, 1)[0]
	tcpPorts := allocateTCPPorts(t, 3)
	const passphrase = "backend-switch-harness-passphrase"
	args := append([]string{
		"start",
		"--config", cfgPath,
		"--mode", "standalone",
		"--data-dir", dataDir,
		"--admin-port", strconv.Itoa(tcpPorts[0]),
		"--grpc-port", strconv.Itoa(tcpPorts[1]),
		"--auth-enabled",
		"--enable-rest",
		"--enable-grpc",
		"--enable-quic", "--quic-port", strconv.Itoa(quicPort),
		"--enable-mcp", "--mcp-port", strconv.Itoa(tcpPorts[2]),
		"--enable-unix", "--unix-socket-path", unixSocket,
	}, ca.tlsFlags(t)...)

	proc := startNode(t, bin, args, fmt.Sprintf("[backend-switch %s] ", backend))
	waitForBarrierStatusReachable(t, ca, tcpPorts[0], 90*time.Second)
	initAndUnsealBarrier(t, ca, tcpPorts[0], passphrase)
	waitForAdmin(t, ca.Pool, tcpPorts[0], 90*time.Second)
	clientCert, clientKey := ca.boundAdminCert(t, dataDir)

	n := &standaloneBackendSwitchNode{
		multiProtoNode: &multiProtoNode{
			cli:        cli,
			ca:         ca,
			dataDir:    dataDir,
			clientCert: clientCert,
			clientKey:  clientKey,
			unixSocket: unixSocket,
			restPort:   tcpPorts[0],
			grpcPort:   tcpPorts[1],
			mcpPort:    tcpPorts[2],
			quicPort:   quicPort,
			passphrase: passphrase,
		},
		bin:     bin,
		args:    args,
		cfgPath: cfgPath,
		proc:    proc,

		adminOnDataPlaneLost: backend == "memory",
	}
	for _, proto := range multiProtocols {
		waitForCLIProtoReady(t, n.multiProtoNode, proto, 60*time.Second)
	}
	return n
}

// restartOnBackend stops the node, rewrites its configuration to name
// backend, starts it again on the same data directory and unseals it.
func (n *standaloneBackendSwitchNode) restartOnBackend(t *testing.T, backend string) {
	t.Helper()
	stopNode(t, n.proc, 15*time.Second)
	writeStandaloneBackendConfig(t, n.cfgPath, backend, "")
	n.proc = startNode(t, n.bin, n.args, fmt.Sprintf("[backend-switch %s] ", backend))
	waitForBarrierStatusReachable(t, n.ca, n.restPort, 90*time.Second)
	unsealOnlyBarrierWithSecret(t, n.ca, n.restPort, []byte(n.passphrase))
	if n.adminOnDataPlaneLost {
		// The admin binding lived on the memory data plane, so the node
		// waits for the ceremony's admin-credential retry before it serves.
		initAndUnsealBarrier(t, n.ca, n.restPort, n.passphrase)
		n.adminOnDataPlaneLost = false
	}
	waitForAdmin(t, n.ca.Pool, n.restPort, 90*time.Second)
	n.clientCert, n.clientKey = n.ca.boundAdminCert(t, n.dataDir)
	for _, proto := range multiProtocols {
		waitForCLIProtoReady(t, n.multiProtoNode, proto, 60*time.Second)
	}
}

// TestCredential_CLI_StoreMoveToDataPlane_PerProtocol stores a credential
// over every transport on a standalone node whose credential store is the
// seal barrier, restarts the node on an encrypting Pebble data plane over the
// same data directory, and retrieves and lists each one over every
// transport: the records moved into the data plane before the first verb.
// A second restart on Pebble serves them again, so they live on the data
// plane rather than in the barrier the move emptied. The source node runs
// the memory backend: it binds the seal barrier exactly as an objstore node
// does (no at-rest Encryptor), and a standalone node declaring an objstore
// database does not boot in this tree (the database factory opens no
// objstore storage).
func TestCredential_CLI_StoreMoveToDataPlane_PerProtocol(t *testing.T) {
	node := startStandaloneBackendSwitchNode(t, "memory", "")

	want := make(map[string]string, len(multiProtocols))
	for _, proto := range multiProtocols {
		name := "moved-" + proto.name
		value := "move-value-" + proto.name
		if out, err := runCLIProto(t, node.multiProtoNode, proto, "credential", "store", name, "--value", value); err != nil {
			t.Fatalf("[%s] credential store on objstore: %v\n%s", proto.name, err, out)
		}
		want[name] = value
	}

	check := func(label string) {
		t.Helper()
		for _, proto := range multiProtocols {
			for name, value := range want {
				out, err := runCLIProto(t, node.multiProtoNode, proto, "credential", "retrieve", name)
				if err != nil {
					t.Fatalf("[%s] credential retrieve %s %s: %v\n%s", proto.name, name, label, err, out)
				}
				if got := strings.TrimSpace(out); got != value {
					t.Fatalf("[%s] credential retrieve %s %s = %q, want %q", proto.name, name, label, got, value)
				}
			}
			out, err := runCLIProto(t, node.multiProtoNode, proto, "credential", "list")
			if err != nil {
				t.Fatalf("[%s] credential list %s: %v\n%s", proto.name, label, err, out)
			}
			for name := range want {
				if !strings.Contains(out, name) {
					t.Fatalf("[%s] credential list %s does not name %q:\n%s", proto.name, label, name, out)
				}
			}
		}
	}

	node.restartOnBackend(t, "pebble")
	check("after the move to pebble")
	node.restartOnBackend(t, "pebble")
	check("after a second pebble restart")
}
