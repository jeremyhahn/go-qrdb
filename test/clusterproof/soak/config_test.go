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

//go:build clusterproof

package soak

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/nemesis"
)

// targetsJSON configures every fault on three nodes. No command in it runs:
// the tests only build the targets.
const targetsJSON = `{
  "exec": ["ssh", "{{.Vars.host}}", "--", "{{shquote .Argv}}"],
  "signal": ["ssh", "{{.Vars.host}}", "--", "kill", "-{{.Signal}}", "1"],
  "restart": ["ssh", "{{.Vars.host}}", "--", "true"],
  "unseal": ["ssh", "{{.Vars.host}}", "--", "true"],
  "nodes": {
    "n1": {"vars": {"host": "db1.example.com"}, "addresses": ["192.0.2.1"]},
    "n2": {"vars": {"host": "db2.example.com"}, "addresses": ["192.0.2.2"]},
    "n3": {"vars": {"host": "db3.example.com"}, "addresses": ["192.0.2.3"]}
  },
  "inspect": {"version": ["qrdbd", "version"]},
  "clock_rate": {"ppm": -5000},
  "disk_full": {"path": "/data/fill"},
  "slow_disk": {"device": "8:0", "cgroup": "/sys/fs/cgroup/qrdb", "bytes_per_second": 1048576},
  "schedule": {"min_gap": "1s", "max_gap": "2s", "min_length": "1s", "max_length": "2s"},
  "heal_timeout": "30s",
  "declared": {"read_mode": "lease"}
}`

const nodesEnv = "n1=db1.example.com:7001,n2=db2.example.com:7001,n3=db3.example.com:7001"

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

var now = time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)

func TestLoadOptionsEveryFault(t *testing.T) {
	o, err := LoadOptions(envOf(map[string]string{
		EnvSeal:     "passphrase",
		EnvNodes:    nodesEnv,
		EnvDuration: "1m",
		EnvSeed:     "99",
		EnvTargets:  writeFile(t, "targets.json", targetsJSON),
	}), now)
	if err != nil {
		t.Fatalf("LoadOptions: %v", err)
	}
	if got, want := slices.Sorted(maps.Keys(o.Nemeses)), slices.Sorted(maps.Keys(faults)); !slices.Equal(got, want) {
		t.Fatalf("faults %q, want every fault %q", got, want)
	}
	if o.Schedule.Seed != 99 || len(o.Schedule.Windows) == 0 {
		t.Fatalf("schedule seed %d with %d windows", o.Schedule.Seed, len(o.Schedule.Windows))
	}
	if len(o.Targets) != 3 || o.Inspector == nil || o.Cluster == nil || o.Replicas == nil || o.Dial == nil {
		t.Fatalf("wiring: %d targets, inspector %v, cluster %v, replicas %v", len(o.Targets), o.Inspector, o.Cluster, o.Replicas)
	}
	if o.Bundle != filepath.Join(DefaultBundleRoot, "2026-10-12T09-30-00Z") {
		t.Fatalf("bundle %q", o.Bundle)
	}
	if o.Workload.Protocol != DefaultProtocol || o.Workload.Seed != 99 || len(o.Workload.Endpoints) != 3 {
		t.Fatalf("workload %+v", o.Workload)
	}
	if o.HealTimeout != 30*time.Second || o.Seal != "passphrase" || o.Declared.ReadMode != "lease" {
		t.Fatalf("heal timeout %s, seal %q, declared %+v", o.HealTimeout, o.Seal, o.Declared)
	}
	// The same seed draws the same schedule.
	again, err := LoadOptions(envOf(map[string]string{
		EnvSeal: "passphrase", EnvNodes: nodesEnv, EnvDuration: "1m", EnvSeed: "99", EnvTargets: writeFile(t, "targets.json", targetsJSON),
	}), now)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(o.Schedule, again.Schedule) {
		t.Fatal("one seed drew two schedules")
	}
}

func TestLoadOptionsNone(t *testing.T) {
	o, err := LoadOptions(envOf(map[string]string{EnvSeal: "none", EnvNodes: "solo=127.0.0.1:7001", EnvNemesis: "none", EnvBundle: "out/x"}), now)
	if err != nil {
		t.Fatalf("LoadOptions: %v", err)
	}
	if len(o.Nemeses) != 0 || len(o.Schedule.Windows) != 0 || o.Targets != nil || o.Schedule.Seed == 0 {
		t.Fatalf("a none run: %d faults, %d windows, seed %d", len(o.Nemeses), len(o.Schedule.Windows), o.Schedule.Seed)
	}
	if o.Bundle != "out/x" || o.Duration != DefaultDuration {
		t.Fatalf("bundle %q, duration %s", o.Bundle, o.Duration)
	}
}

func TestLoadOptionsWorkloadFile(t *testing.T) {
	good := `{"endpoints": ["db1.example.com:7001"], "protocol": "grpc", "clients": 3, "keys": 5}`
	o, err := LoadOptions(envOf(map[string]string{
		EnvSeal: "none", EnvNodes: "n1=db1.example.com:7001", EnvNemesis: "none", EnvWorkload: writeFile(t, "w.json", good),
	}), now)
	if err != nil {
		t.Fatalf("LoadOptions: %v", err)
	}
	if o.Workload.Protocol != "grpc" || o.Workload.Clients != 3 {
		t.Fatalf("workload %+v", o.Workload)
	}
}

// restartOnly is a node that can be killed and restarted but not unsealed.
type restartOnly struct{ fakeTarget }

var _ nemesis.Restarter = restartOnly{}

func (restartOnly) Signal(context.Context, syscall.Signal) error { return nil }
func (restartOnly) Restart(context.Context) error                { return nil }

func TestLoadOptionsKillMatchesTheSeal(t *testing.T) {
	for seal, sealed := range sealTypes {
		t.Run(seal, func(t *testing.T) {
			o, err := LoadOptions(envOf(map[string]string{
				EnvSeal: seal, EnvNodes: nodesEnv, EnvNemesis: "kill", EnvDuration: "1m", EnvTargets: writeFile(t, "t.json", targetsJSON),
			}), now)
			if err != nil {
				t.Fatalf("LoadOptions: %v", err)
			}
			if o.Seal != seal {
				t.Fatalf("seal %q, want %q", o.Seal, seal)
			}
			// A kill refuses, before it signals anything, a target its heal
			// cannot bring back: one that cannot unseal shows which kill
			// the seal type chose.
			err = o.Nemeses["kill"].Inject(context.Background(), []nemesis.Target{restartOnly{fakeTarget{"n1"}}})
			if refused := errors.Is(err, nemesis.ErrUnsupported); refused != sealed {
				t.Fatalf("Inject = %v: refused %t, want %t", err, refused, sealed)
			}
		})
	}
}

func TestLoadOptionsKillUnderNoneNeedsNoUnseal(t *testing.T) {
	tf := writeFile(t, "t.json", strings.Replace(targetsJSON, `  "unseal": ["ssh", "{{.Vars.host}}", "--", "true"],
`, "", 1))
	env := func(seal string) func(string) string {
		return envOf(map[string]string{EnvSeal: seal, EnvNodes: nodesEnv, EnvNemesis: "kill", EnvDuration: "1m", EnvTargets: tf})
	}
	o, err := LoadOptions(env("none"), now)
	if err != nil {
		t.Fatalf("LoadOptions: %v", err)
	}
	if _, ok := o.Nemeses["kill"]; !ok {
		t.Fatalf("faults %v, want kill", slices.Sorted(maps.Keys(o.Nemeses)))
	}
	for _, seal := range []string{"passphrase", "shamir"} {
		_, err := LoadOptions(env(seal), now)
		if _, ok := errors.AsType[*ConfigError](err); !ok || !strings.Contains(err.Error(), EnvTargets+" unseal") {
			t.Errorf("LoadOptions of kill under %s without an unseal command = %v, want a ConfigError naming %s unseal", seal, err, EnvTargets)
		}
	}
}

// countingNode is a node that can be killed, restarted and unsealed, and
// counts each restart and unseal its heal asks for.
type countingNode struct {
	fakeTarget
	restarts, unseals int
}

var _ nemesis.Unsealer = (*countingNode)(nil)

func (*countingNode) Signal(context.Context, syscall.Signal) error { return nil }
func (n *countingNode) Restart(context.Context) error              { n.restarts++; return nil }
func (n *countingNode) Unseal(context.Context) error               { n.unseals++; return nil }

// TestLoadOptionsKillHealMatchesTheSeal proves the restart path follows the
// seal type on a node that could do either: a none node is restarted and left
// to unseal itself, a keyed one is restarted and then unsealed through the
// template.
func TestLoadOptionsKillHealMatchesTheSeal(t *testing.T) {
	for seal, sealed := range sealTypes {
		t.Run(seal, func(t *testing.T) {
			o, err := LoadOptions(envOf(map[string]string{
				EnvSeal: seal, EnvNodes: nodesEnv, EnvNemesis: "kill", EnvDuration: "1m", EnvTargets: writeFile(t, "t.json", targetsJSON),
			}), now)
			if err != nil {
				t.Fatalf("LoadOptions: %v", err)
			}
			n := &countingNode{fakeTarget: fakeTarget{"n1"}}
			kill := o.Nemeses["kill"]
			if err := kill.Inject(context.Background(), []nemesis.Target{n}); err != nil {
				t.Fatalf("Inject: %v", err)
			}
			if err := kill.Heal(context.Background()); err != nil {
				t.Fatalf("Heal: %v", err)
			}
			want := 0
			if sealed {
				want = 1
			}
			if n.restarts != 1 || n.unseals != want {
				t.Fatalf("restarts %d, unseals %d; want 1, %d", n.restarts, n.unseals, want)
			}
		})
	}
}

func TestLoadOptionsRefuses(t *testing.T) {
	targets := func(t *testing.T, edit func(string) string) string {
		return writeFile(t, "targets.json", edit(targetsJSON))
	}
	cases := map[string]struct {
		env   func(t *testing.T) map[string]string
		field string
	}{
		"no nodes": {func(*testing.T) map[string]string { return map[string]string{} }, EnvNodes},
		"bad node": {func(*testing.T) map[string]string { return map[string]string{EnvSeal: "none", EnvNodes: "n1"} }, EnvNodes},
		"bad name": {func(*testing.T) map[string]string { return map[string]string{EnvSeal: "none", EnvNodes: "../n=a"} }, EnvNodes},
		"twice":    {func(*testing.T) map[string]string { return map[string]string{EnvSeal: "none", EnvNodes: "n=a,n=b"} }, EnvNodes},
		"no seal":  {func(*testing.T) map[string]string { return map[string]string{EnvNodes: "n=a", EnvNemesis: "none"} }, EnvSeal},
		"unknown seal": {func(*testing.T) map[string]string {
			return map[string]string{EnvSeal: "tpm2", EnvNodes: "n=a", EnvNemesis: "none"}
		}, EnvSeal},
		"bad duration": {func(*testing.T) map[string]string {
			return map[string]string{EnvSeal: "none", EnvNodes: "n=a", EnvDuration: "-1s"}
		}, EnvDuration},
		"unknown fault": {func(*testing.T) map[string]string {
			return map[string]string{EnvSeal: "none", EnvNodes: "n=a", EnvNemesis: "meteor"}
		}, EnvNemesis},
		"none and a fault": {func(*testing.T) map[string]string {
			return map[string]string{EnvSeal: "none", EnvNodes: "n=a", EnvNemesis: "none,kill"}
		}, EnvNemesis},
		"fault twice": {func(*testing.T) map[string]string {
			return map[string]string{EnvSeal: "none", EnvNodes: "n=a", EnvNemesis: "kill,kill"}
		}, EnvNemesis},
		"bad seed": {func(*testing.T) map[string]string {
			return map[string]string{EnvSeal: "none", EnvNodes: "n=a", EnvNemesis: "none", EnvSeed: "x"}
		}, EnvSeed},
		"fault without targets": {func(*testing.T) map[string]string {
			return map[string]string{EnvSeal: "none", EnvNodes: "n=a", EnvNemesis: "kill"}
		}, EnvTargets},
		"unknown targets field": {func(t *testing.T) map[string]string {
			return map[string]string{EnvSeal: "none", EnvNodes: nodesEnv, EnvNemesis: "kill", EnvTargets: targets(t, func(s string) string { return strings.Replace(s, `"exec"`, `"exe"`, 1) })}
		}, EnvTargets},
		"targets name other nodes": {func(t *testing.T) map[string]string {
			return map[string]string{EnvSeal: "none", EnvNodes: "n1=a,n2=b", EnvNemesis: "kill", EnvTargets: writeFile(t, "t.json", targetsJSON)}
		}, EnvTargets},
		"too few nodes for a partition": {func(t *testing.T) map[string]string {
			return map[string]string{EnvSeal: "none", EnvNodes: "n1=a,n2=b", EnvNemesis: "partition-bridge", EnvTargets: targets(t, func(s string) string {
				return strings.Replace(s, `,
    "n3": {"vars": {"host": "db3.example.com"}, "addresses": ["192.0.2.3"]}`, "", 1)
			})}
		}, EnvNemesis},
		"partition without addresses": {func(t *testing.T) map[string]string {
			return map[string]string{EnvSeal: "none", EnvNodes: nodesEnv, EnvNemesis: "partition-majority-minority", EnvTargets: targets(t, func(s string) string {
				return strings.Replace(s, `, "addresses": ["192.0.2.3"]`, "", 1)
			})}
		}, EnvNemesis},
		"clock rate without ppm": {func(t *testing.T) map[string]string {
			return map[string]string{EnvSeal: "none", EnvNodes: nodesEnv, EnvNemesis: "clock-rate", EnvTargets: targets(t, func(s string) string {
				return strings.Replace(s, `"ppm": -5000`, `"ppm": 0`, 1)
			})}
		}, EnvNemesis},
		"clock rate out of range": {func(t *testing.T) map[string]string {
			return map[string]string{EnvSeal: "none", EnvNodes: nodesEnv, EnvNemesis: "clock-rate", EnvTargets: targets(t, func(s string) string {
				return strings.Replace(s, `"ppm": -5000`, `"ppm": -100100`, 1)
			})}
		}, EnvNemesis},
		"clock rate command without its flags": {func(t *testing.T) map[string]string {
			return map[string]string{EnvSeal: "none", EnvNodes: nodesEnv, EnvNemesis: "clock-rate", EnvTargets: targets(t, func(s string) string {
				return strings.Replace(s, `"ppm": -5000`, `"ppm": -5000, "command": ["clusterproof", "clock-rate"]`, 1)
			})}
		}, EnvNemesis},
		"disk full without path": {func(t *testing.T) map[string]string {
			return map[string]string{EnvSeal: "none", EnvNodes: nodesEnv, EnvNemesis: "disk-full", EnvTargets: targets(t, func(s string) string {
				return strings.Replace(s, `"path": "/data/fill"`, `"path": ""`, 1)
			})}
		}, EnvNemesis},
		"slow disk without device": {func(t *testing.T) map[string]string {
			return map[string]string{EnvSeal: "none", EnvNodes: nodesEnv, EnvNemesis: "slow-disk", EnvTargets: targets(t, func(s string) string {
				return strings.Replace(s, `"device": "8:0"`, `"device": ""`, 1)
			})}
		}, EnvNemesis},
		"schedule longer than the run": {func(t *testing.T) map[string]string {
			return map[string]string{EnvSeal: "none", EnvNodes: nodesEnv, EnvNemesis: "kill", EnvDuration: "500ms", EnvTargets: writeFile(t, "t.json", targetsJSON)}
		}, EnvDuration},
		"workload endpoints differ": {func(t *testing.T) map[string]string {
			return map[string]string{EnvSeal: "none", EnvNodes: "n1=a", EnvNemesis: "none", EnvWorkload: writeFile(t, "w.json", `{"endpoints": ["b"]}`)}
		}, EnvWorkload},
		"workload duration differs": {func(t *testing.T) map[string]string {
			return map[string]string{EnvSeal: "none", EnvNodes: "n1=a", EnvNemesis: "none", EnvWorkload: writeFile(t, "w.json", `{"endpoints": ["a"], "duration": "1s"}`)}
		}, EnvWorkload},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadOptions(envOf(c.env(t)), now)
			if err == nil {
				t.Fatal("LoadOptions accepted an invalid environment")
			}
			ce, ok := errors.AsType[*ConfigError](err)
			if !ok || !strings.HasPrefix(ce.Field, c.field) {
				t.Fatalf("LoadOptions = %v, want a ConfigError on %s", err, c.field)
			}
		})
	}
}

func TestParseNemesisIsOrderFree(t *testing.T) {
	a, err := parseNemesis("pause, kill")
	if err != nil {
		t.Fatal(err)
	}
	b, err := parseNemesis("kill,pause")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a, b) || !slices.Equal(a, []string{"kill", "pause"}) {
		t.Fatalf("parseNemesis: %q and %q", a, b)
	}
}

func TestParseSeedDraws(t *testing.T) {
	for range 16 {
		s, err := parseSeed("")
		if err != nil || s <= 0 {
			t.Fatalf("parseSeed drew %d, %v", s, err)
		}
	}
}

func TestValidNodeName(t *testing.T) {
	for name, want := range map[string]bool{
		"n1": true, "db-1.example_com": true, "9": true,
		"": false, "-n": false, ".n": false, "_n": false, "n/1": false, "n 1": false, "nø": false,
		strings.Repeat("n", maxNodeName): true, strings.Repeat("n", maxNodeName+1): false,
	} {
		if got := validNodeName(name); got != want {
			t.Errorf("validNodeName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestMainRefusesInvalidConfiguration(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), Env{Getenv: envOf(nil), Stdout: &stdout, Stderr: &stderr})
	if code != ExitInvalid || !strings.Contains(stderr.String(), EnvNodes) {
		t.Fatalf("Main = %d, stderr %q", code, stderr.String())
	}
}

func TestMainRunsAndReports(t *testing.T) {
	kv := newFakeKV()
	bundle := filepath.Join(t.TempDir(), "bundle")
	var stdout, stderr bytes.Buffer
	code := Main(context.Background(), Env{
		Getenv: envOf(map[string]string{
			EnvSeal: "shamir", EnvNodes: "n1=a1", EnvNemesis: "none", EnvDuration: "200ms", EnvBundle: bundle, EnvSeed: "5",
		}),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Harness: Harness{Version: "v-test"},
		Configure: func(o *Options) error {
			o.Dial, o.Cluster, o.Replicas = kv.dial, fakeCluster{leader: "n1"}, agreeing
			o.Checkers = append(o.Checkers, passChecker{})
			return nil
		},
	})
	if code != ExitInvalid {
		t.Fatalf("a none run = %d, want %d; stdout %q stderr %q", code, ExitInvalid, stdout.String(), stderr.String())
	}
	for _, want := range []string{"faults [none], seed 5", "invalid under seal shamir (exit 2)", "no nemesis configured", "E1  pass", "bundle: " + bundle, "manifest sha256:"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout.String())
		}
	}
	var m Manifest
	if err := decodeMember(bundle, fileManifest, &m); err != nil || m.Harness.Version != "v-test" {
		t.Fatalf("manifest harness %+v, %v", m.Harness, err)
	}
}

func TestMainConfigureError(t *testing.T) {
	var stderr bytes.Buffer
	code := Main(context.Background(), Env{
		Getenv:    envOf(map[string]string{EnvSeal: "none", EnvNodes: "n1=a1", EnvNemesis: "none"}),
		Stdout:    &bytes.Buffer{},
		Stderr:    &stderr,
		Configure: func(*Options) error { return errors.New("no checker library") },
	})
	if code != ExitInvalid || !strings.Contains(stderr.String(), "no checker library") {
		t.Fatalf("Main = %d, stderr %q", code, stderr.String())
	}
}
