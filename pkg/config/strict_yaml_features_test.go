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

// TestLoad_TypoInMergeSequence_Refused is the "<<: [*a, *b]" sequence form
// of a merge key: an unknown key inside any merged-in mapping is refused with
// its path, exactly as in the single-mapping "<<: *a" form.
func TestLoad_TypoInMergeSequence_Refused(t *testing.T) {
	path := writeTemp(t, `
storage:
  objstore:
    settings:
      good: &good
        backend: "pebble"
      bad: &bad
        bakend: "pebble"
databases:
  - <<: [*good, *bad]
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

// TestResolveAlias_Nil covers resolveAlias's own nil guard directly: a nil
// node is not an alias to resolve, and must come back nil rather than
// panic on a nil receiver.
func TestResolveAlias_Nil(t *testing.T) {
	if got := resolveAlias(nil); got != nil {
		t.Errorf("resolveAlias(nil): got %v, want nil", got)
	}
}

// TestCheckMergeValue_CyclicAlias_ReturnsNil covers checkMergeValue's own
// top-level resolveAlias guard (distinct from the per-element one the
// sequence case also runs): a "<<" value that itself resolves to nil --
// built by hand, as the cycle is never reachable through real YAML text --
// must be treated as nothing to merge, not mishandled.
func TestCheckMergeValue_CyclicAlias_ReturnsNil(t *testing.T) {
	cyclic := &yaml.Node{Kind: yaml.AliasNode}
	cyclic.Alias = cyclic
	if err := checkMergeValue(cyclic, reflect.TypeOf(Config{}), "", "file.yaml"); err != nil {
		t.Errorf("checkMergeValue on a cyclic alias: got %v, want nil", err)
	}
}

// TestCheckMergeValue_ScalarValue_ReturnsNil covers checkMergeValue's
// default case: a "<<" value that is neither a mapping nor a sequence (a
// malformed "<<: some_string") carries no schema of its own to check --
// that mismatch is the decoder's concern, not an unknown key.
func TestCheckMergeValue_ScalarValue_ReturnsNil(t *testing.T) {
	scalar := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "oops"}
	if err := checkMergeValue(scalar, reflect.TypeOf(Config{}), "", "file.yaml"); err != nil {
		t.Errorf("checkMergeValue on a scalar merge value: got %v, want nil", err)
	}
}

// TestCheckMergeValue_Sequence_SkipsNilAndNonMappingThenSucceeds covers the
// elements of a "<<: [...]" sequence that real YAML text cannot produce and
// so TestLoad_TypoInMergeSequence_Refused cannot reach: a cyclic self-alias
// element (resolves to nil) and a scalar element are skipped, and a mapping
// element with every key known passes.
func TestCheckMergeValue_Sequence_SkipsNilAndNonMappingThenSucceeds(t *testing.T) {
	cyclic := &yaml.Node{Kind: yaml.AliasNode}
	cyclic.Alias = cyclic
	scalar := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "oops"}
	var good yaml.Node
	if err := yaml.Unmarshal([]byte("name: ok\nbackend: pebble\n"), &good); err != nil {
		t.Fatal(err)
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{cyclic, scalar, good.Content[0]}}
	if err := checkMergeValue(seq, reflect.TypeOf(DatabaseConfig{}), "databases[0]", "file.yaml"); err != nil {
		t.Errorf("checkMergeValue on a sequence with a nil and a non-mapping element: got %v, want nil (both skipped, the mapping element has no unknown key)", err)
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

// CycleFixtureA and CycleFixtureB inline each other through anonymous
// pointer fields -- legal Go (a pointer's size does not depend on what it
// points to) and the only shape that can reach addYAMLFields' own cycle
// guard, since two struct VALUE types cannot embed each other at all. Both
// must be EXPORTED types: an embedded field's own export-ness follows its
// type's name, not the field's, so embedding an unexported type would make
// addYAMLFields' unexported-field guard skip it before the cycle guard is
// ever reached. Exercises both the pointer-dereference loop that unwraps an
// inlined *T before checking whether T is a struct, and the visited-cycle
// guard itself (the second time either type is reached, through the other).
type CycleFixtureA struct {
	*CycleFixtureB `yaml:",inline"`
	A              string `yaml:"a"`
}

type CycleFixtureB struct {
	*CycleFixtureA `yaml:",inline"`
	B              string `yaml:"b"`
}

// TestStructYAMLFields_InlineCycle_FlattensWithoutInfiniteRecursion proves
// structYAMLFields terminates on a mutual pointer-inline cycle and still
// flattens both types' own fields into one map -- the cycle guard stops
// addYAMLFields's second, redundant descent, not the first useful one.
func TestStructYAMLFields_InlineCycle_FlattensWithoutInfiniteRecursion(t *testing.T) {
	fields := structYAMLFields(reflect.TypeOf(CycleFixtureA{}))
	if _, ok := fields["a"]; !ok {
		t.Error(`fields["a"] missing, want present`)
	}
	if _, ok := fields["b"]; !ok {
		t.Error(`fields["b"] missing, want present (promoted through the inline cycle)`)
	}
	if len(fields) != 2 {
		t.Errorf("fields: got %d entries %v, want exactly 2 (\"a\" and \"b\")", len(fields), fields)
	}
}

// dashAndUnexportedFixture carries one field this package must never treat
// as a known YAML key (an explicit "yaml:\"-\"" opt-out) and one it cannot
// even see as a YAML key in the first place (unexported, so not decodable
// by Viper's mapstructure step either).
type dashAndUnexportedFixture struct {
	Visible string `yaml:"visible"`
	Hidden  string `yaml:"-"`
	secret  string
}

// TestAddYAMLFields_DashTagAndUnexportedField_Skipped covers both of
// addYAMLFields' own skip branches in one fixture: an unexported field
// (PkgPath != "") and a field whose tag opts out via yamlFieldName's
// ok=false return. Neither must appear in the known-field set; "visible"
// must.
func TestAddYAMLFields_DashTagAndUnexportedField_Skipped(t *testing.T) {
	v := dashAndUnexportedFixture{Visible: "v", Hidden: "h", secret: "s"}
	_ = v.secret // referenced so the field is not flagged dead code; addYAMLFields reaches it via reflect regardless of use
	fields := structYAMLFields(reflect.TypeOf(v))
	if _, ok := fields["visible"]; !ok {
		t.Error(`fields["visible"] missing, want present`)
	}
	if _, ok := fields["hidden"]; ok {
		t.Error(`fields["hidden"] present, want absent (yaml:"-" opts out)`)
	}
	if _, ok := fields["secret"]; ok {
		t.Error(`fields["secret"] present, want absent (unexported)`)
	}
}

// TestYamlFieldName pins how a struct field's tag maps to its YAML key: no
// tag or an empty name lowers the Go field name, an exact "-" opts out, and
// an anonymous field with an empty name is inlined.
func TestYamlFieldName(t *testing.T) {
	type leaf struct {
		X string `yaml:"x"`
	}
	for _, tc := range []struct {
		name       string
		field      reflect.StructField
		wantName   string
		wantInline bool
		wantOK     bool
	}{
		{"no tag", reflect.TypeOf(struct{ Foo string }{}).Field(0), "foo", false, true},
		{"exact dash", reflect.TypeOf(struct {
			Foo string `yaml:"-"`
		}{}).Field(0), "", false, false},
		{"empty name, anonymous", reflect.TypeOf(struct {
			leaf `yaml:",omitempty"`
		}{}).Field(0), "", true, true},
		{"empty name, named field", reflect.TypeOf(struct {
			Foo string `yaml:",omitempty"`
		}{}).Field(0), "foo", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name, inline, ok := yamlFieldName(tc.field)
			if !tc.wantOK {
				if ok {
					t.Errorf("yamlFieldName: got ok=true (name=%q, inline=%v), want ok=false", name, inline)
				}
				return
			}
			if name != tc.wantName || inline != tc.wantInline || !ok {
				t.Errorf("yamlFieldName: got (%q, %v, %v), want (%q, %v, true)", name, inline, ok, tc.wantName, tc.wantInline)
			}
		})
	}
}
