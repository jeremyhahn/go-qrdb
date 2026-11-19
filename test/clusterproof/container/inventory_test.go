//go:build clusterproof

package container

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func encode(t *testing.T, inv any) string {
	t.Helper()
	b, err := json.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseInventoryDefaults(t *testing.T) {
	inv := testInventory(t)
	inv.Docker, inv.Network = nil, ""
	got, err := ParseInventory(strings.NewReader(encode(t, inv)))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Docker, DefaultDocker) || got.Network != HostNetwork {
		t.Errorf("defaults: docker %v network %q", got.Docker, got.Network)
	}
	if got.Nodes[1].Vars["host"] != "h2" || got.FirstBoot.SecretFile != inv.FirstBoot.SecretFile {
		t.Errorf("fields lost: %+v", got)
	}
}

func TestParseInventoryKeepsSettings(t *testing.T) {
	inv := testInventory(t)
	inv.Docker = []string{"ssh", "{{.Vars.host}}", "docker", "$@"}
	inv.Network = "proof"
	got, err := ParseInventory(strings.NewReader(encode(t, inv)))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Docker, inv.Docker) || got.Network != "proof" {
		t.Errorf("settings: docker %v network %q", got.Docker, got.Network)
	}
}

func TestParseInventoryRejects(t *testing.T) {
	for name, mutate := range map[string]func(*Inventory){
		"image":       func(inv *Inventory) { inv.Image = "" },
		"ca":          func(inv *Inventory) { inv.Credentials.CAFile = "" },
		"key":         func(inv *Inventory) { inv.Credentials.KeyFile = "" },
		"so pin":      func(inv *Inventory) { inv.FirstBoot.SOPINFile = "" },
		"user pin":    func(inv *Inventory) { inv.FirstBoot.UserPINFile = "" },
		"two nodes":   func(inv *Inventory) { inv.Nodes = inv.Nodes[:2] },
		"name":        func(inv *Inventory) { inv.Nodes[0].Name = "a b" },
		"replica":     func(inv *Inventory) { inv.Nodes[0].ReplicaID = 0 },
		"address":     func(inv *Inventory) { inv.Nodes[0].Address = "" },
		"tls":         func(inv *Inventory) { inv.Nodes[0].TLSDir = "" },
		"data":        func(inv *Inventory) { inv.Nodes[0].Data = "" },
		"dup name":    func(inv *Inventory) { inv.Nodes[1].Name = inv.Nodes[0].Name },
		"dup replica": func(inv *Inventory) { inv.Nodes[1].ReplicaID = inv.Nodes[0].ReplicaID },
		"dup address": func(inv *Inventory) { inv.Nodes[1].Address = inv.Nodes[0].Address },
		"bridge by name": func(inv *Inventory) {
			inv.Network = "proof"
			inv.Nodes[0].Address = "node-a"
		},
	} {
		t.Run(name, func(t *testing.T) {
			inv := testInventory(t)
			mutate(&inv)
			var ce *ConfigError
			if _, err := ParseInventory(strings.NewReader(encode(t, inv))); !errors.As(err, &ce) {
				t.Fatalf("got %v, want a ConfigError", err)
			}
		})
	}
}

func TestParseInventoryRejectsBadJSON(t *testing.T) {
	for name, doc := range map[string]string{
		"unknown field": `{"image":"x","imgae":"y"}`,
		"not json":      `{`,
	} {
		var ce *ConfigError
		if _, err := ParseInventory(strings.NewReader(doc)); !errors.As(err, &ce) || ce.Field != "json" {
			t.Errorf("%s: got %v, want a json ConfigError", name, err)
		}
	}
}

func TestLoadInventory(t *testing.T) {
	inv := testInventory(t)
	path := filepath.Join(t.TempDir(), "inventory.json")
	if err := os.WriteFile(path, []byte(encode(t, inv)), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadInventory(path)
	if err != nil || len(got.Nodes) != 3 {
		t.Fatalf("LoadInventory: %v, %d nodes", err, len(got.Nodes))
	}
	var ce *ConfigError
	if _, err := LoadInventory(filepath.Join(t.TempDir(), "missing.json")); !errors.As(err, &ce) || ce.Field != "path" {
		t.Errorf("missing file: got %v", err)
	}
}

func TestNodeAddresses(t *testing.T) {
	n := Node{Name: "n1", Address: "10.0.0.1"}
	if n.Container() != "clusterproof-n1" || n.RaftAddress() != "10.0.0.1:63001" {
		t.Errorf("container %q raft %q", n.Container(), n.RaftAddress())
	}
	want := map[string]string{"quic": "10.0.0.1:63004", "grpc": "10.0.0.1:63003", "rest": "https://10.0.0.1:63002"}
	for _, p := range Protocols {
		if got, err := n.Endpoint(p); err != nil || got != want[p] {
			t.Errorf("%s: %q, %v", p, got, err)
		}
	}
	v6 := Node{Address: "fd00::1"}
	if got, _ := v6.Endpoint("rest"); got != "https://[fd00::1]:63002" {
		t.Errorf("ipv6 rest endpoint %q", got)
	}
	var upe *UnknownProtocolError
	if _, err := n.Endpoint("http"); !errors.As(err, &upe) {
		t.Errorf("unknown protocol: got %v", err)
	}
}

func TestReadSecret(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	got, err := readSecret("f", write("pin", "1234\r\n"))
	if err != nil || string(got) != "1234" {
		t.Errorf("trimmed: %q, %v", got, err)
	}
	got, err = readSecret("f", write("inner", "a\nb"))
	if err != nil || string(got) != "a\nb" {
		t.Errorf("inner newline kept: %q, %v", got, err)
	}
	var ce *ConfigError
	if _, err := readSecret("f", write("empty", "\n")); !errors.As(err, &ce) {
		t.Errorf("empty: got %v", err)
	}
}
