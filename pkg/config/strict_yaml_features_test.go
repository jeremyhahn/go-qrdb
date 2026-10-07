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

package config

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// TestLoad_AnchorAndMerge_Loads is standard YAML that both yaml.v3 and
// Viper's mapstructure decode honour: an anchor on one databases[] entry,
// merged into its siblings with "<<". It must load AND decode correctly --
// "cache" overrides the merged-in backend with its own sibling key, which
// is exactly what YAML merge semantics promise (a key already present in
// the mapping wins over one brought in by "<<").
func TestLoad_AnchorAndMerge_Loads(t *testing.T) {
	path := writeTemp(t, `
raft:
  data_dir: "/tmp/anchor-merge"
  shard_id: 1
  replica_id: 1
  raft_address: "127.0.0.1:63001"
databases:
  - &base
    name: "base"
    backend: "pebble"
  - <<: *base
    name: "app"
  - <<: *base
    name: "cache"
    backend: "memory"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: unexpected error: %v", err)
	}
	if len(cfg.Databases) != 3 {
		t.Fatalf("Databases: got %d entries, want 3", len(cfg.Databases))
	}
	if got := cfg.Databases[1]; got.Name != "app" || got.Backend != "pebble" {
		t.Errorf("Databases[1] (merged from the anchor): got %+v", got)
	}
	if got := cfg.Databases[2]; got.Name != "cache" || got.Backend != "memory" {
		t.Errorf("Databases[2] (merged, backend overridden by a sibling key): got %+v", got)
	}
}

// TestLoad_TypoInAliasedBlock_Refused covers a value reached purely through
// an alias ("tls: *common_tls"), as opposed to a merge. The aliased block
// is anchored inside storage.objstore.settings -- a map[string]string
// field, deliberately unchecked by this package's own design -- so the
// ONLY path that can catch the typo is resolving the "tls: *bad_tls"
// alias and validating what it resolves to.
func TestLoad_TypoInAliasedBlock_Refused(t *testing.T) {
	path := writeTemp(t, `
storage:
  objstore:
    settings:
      base: &bad_tls
        enabled: true
        minn_version: "TLS1.3"
tls: *bad_tls
`)
	_, err := Load(path)
	var ufe *UnknownFieldError
	if !errors.As(err, &ufe) {
		t.Fatalf("Load: got %v (%T), want *UnknownFieldError", err, err)
	}
	if ufe.Path != "tls.minn_version" {
		t.Errorf("Path: got %q, want %q", ufe.Path, "tls.minn_version")
	}
}

// TestLoad_TypoInMergedBlock_Refused is TestLoad_TypoInAliasedBlock_Refused's
// counterpart for "<<" rather than a plain alias: the typo'd block is
// merged into a databases[] entry rather than assigned directly.
func TestLoad_TypoInMergedBlock_Refused(t *testing.T) {
	path := writeTemp(t, `
storage:
  objstore:
    settings:
      base: &base
        bakend: "pebble"
databases:
  - <<: *base
    name: "app"
`)
	_, err := Load(path)
	var ufe *UnknownFieldError
	if !errors.As(err, &ufe) {
		t.Fatalf("Load: got %v (%T), want *UnknownFieldError", err, err)
	}
	if ufe.Path != "databases[0].bakend" {
		t.Errorf("Path: got %q, want %q", ufe.Path, "databases[0].bakend")
	}
}

// TestResolveAlias_Chain covers an alias pointing at another alias rather
// than directly at a resolved value -- resolveAlias must follow the whole
// chain, not stop one hop in.
func TestResolveAlias_Chain(t *testing.T) {
	leaf := &yaml.Node{Kind: yaml.ScalarNode, Value: "x"}
	mid := &yaml.Node{Kind: yaml.AliasNode, Alias: leaf}
	top := &yaml.Node{Kind: yaml.AliasNode, Alias: mid}
	if got := resolveAlias(top); got != leaf {
		t.Errorf("resolveAlias: got %v, want the chain's leaf %v", got, leaf)
	}
}

// TestResolveAlias_Cycle is the direct unit test for the cycle guard:
// resolveAlias must return nil rather than loop forever. Not reachable
// through real YAML text -- an alias can only name an anchor that already
// appeared earlier in the document -- so the cycle is built by hand.
func TestResolveAlias_Cycle(t *testing.T) {
	a := &yaml.Node{Kind: yaml.AliasNode}
	b := &yaml.Node{Kind: yaml.AliasNode}
	a.Alias = b
	b.Alias = a
	if got := resolveAlias(a); got != nil {
		t.Errorf("resolveAlias on a cycle: got %v, want nil", got)
	}
}

// TestCheckNode_AliasCycleDoesNotHang is the same cycle driven through
// checkNode's full dispatch, bounded by a hard timeout: a regression that
// reintroduced an infinite loop must fail this test loudly rather than
// hang the rest of the suite.
func TestCheckNode_AliasCycleDoesNotHang(t *testing.T) {
	a := &yaml.Node{Kind: yaml.AliasNode}
	b := &yaml.Node{Kind: yaml.AliasNode}
	a.Alias = b
	b.Alias = a

	done := make(chan error, 1)
	go func() {
		done <- checkNode(a, reflect.TypeOf(Config{}), "", "file.yaml")
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("checkNode on a cyclic alias: got %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("checkNode on a cyclic alias did not return within 2s -- it hung")
	}
}

// InlineLeafFixture is a small struct embedded two different ways by the
// fixtures below, to exercise structYAMLFields' inline/anonymous-embedding
// flattening through checkNode -- independent of the real Config schema,
// which has neither today.
type InlineLeafFixture struct {
	Foo string `yaml:"foo"`
}

// InlineAnonFixture embeds InlineLeafFixture anonymously with no yaml tag
// of its own, which yaml.v3 treats as inlined by default: an anonymous
// field is promoted unless its tag gives it an explicit name.
type InlineAnonFixture struct {
	InlineLeafFixture
	Bar string `yaml:"bar"`
}

// InlineTaggedFixture embeds the same leaf as a named, non-anonymous field
// carrying an explicit ",inline" option -- yaml.v3's inline mechanism for
// a field that is not embedded.
type InlineTaggedFixture struct {
	Leaf InlineLeafFixture `yaml:",inline"`
	Bar  string            `yaml:"bar"`
}

// TestCheckNode_InlineAndAnonymousEmbedding_FieldsAreKnown proves
// structYAMLFields flattens both shapes into the parent's own known-key
// set -- "foo" is accepted at the top level, not only under "InlineLeafFixture"
// or "Leaf" -- and that this does not become a blanket pass: a key that
// genuinely isn't on either the fixture or its embedded/inlined leaf is
// still refused, naming its (unprefixed) path.
func TestCheckNode_InlineAndAnonymousEmbedding_FieldsAreKnown(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  reflect.Type
	}{
		{"anonymous embedding", reflect.TypeOf(InlineAnonFixture{})},
		{"explicit inline tag", reflect.TypeOf(InlineTaggedFixture{})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var node yaml.Node
			if err := yaml.Unmarshal([]byte("foo: x\nbar: y\n"), &node); err != nil {
				t.Fatal(err)
			}
			if err := checkNode(node.Content[0], tc.typ, "", "file.yaml"); err != nil {
				t.Errorf("checkNode: got %v, want nil (foo is promoted from the embedded/inlined leaf)", err)
			}

			var bad yaml.Node
			if err := yaml.Unmarshal([]byte("foo: x\nbaz: z\n"), &bad); err != nil {
				t.Fatal(err)
			}
			err := checkNode(bad.Content[0], tc.typ, "", "file.yaml")
			var ufe *UnknownFieldError
			if !errors.As(err, &ufe) {
				t.Fatalf("checkNode: got %v (%T), want *UnknownFieldError for the genuinely unknown key", err, err)
			}
			if ufe.Path != "baz" {
				t.Errorf("Path: got %q, want %q", ufe.Path, "baz")
			}
		})
	}
}
