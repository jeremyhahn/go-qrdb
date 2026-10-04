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

	"gopkg.in/yaml.v3"
)

// TestResolveAlias_Nil covers resolveAlias's own nil guard directly: a nil
// node is not an alias to resolve, and must come back nil rather than
// panic on a nil receiver.
func TestResolveAlias_Nil(t *testing.T) {
	if got := resolveAlias(nil); got != nil {
		t.Errorf("resolveAlias(nil): got %v, want nil", got)
	}
}

// TestCheckStructNode_NonMappingValue_NoError covers checkStructNode's type
// guard: a struct-typed field given a non-mapping value (here, YAML null,
// as "server:" with nothing after it parses to) is a type mismatch for
// Validate/mapstructure to report, not an unknown key -- checkStructNode
// must return nil rather than mishandle the node's absent Content slice.
func TestCheckStructNode_NonMappingValue_NoError(t *testing.T) {
	null := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	if err := checkStructNode(null, reflect.TypeOf(ServerConfig{}), "server", "file.yaml"); err != nil {
		t.Errorf("checkStructNode on a non-mapping node: got %v, want nil", err)
	}
}

// TestCheckSliceNode_NonSequenceValue_NoError is checkStructNode's non-mapping
// case above, for a slice-typed field: "databases:" with no value parses as
// YAML null rather than a sequence, and checkSliceNode must return nil
// rather than index into a sequence that is not there.
func TestCheckSliceNode_NonSequenceValue_NoError(t *testing.T) {
	null := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	if err := checkSliceNode(null, reflect.TypeOf(DatabaseConfig{}), "databases", "file.yaml"); err != nil {
		t.Errorf("checkSliceNode on a non-sequence node: got %v, want nil", err)
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
// that mismatch is Validate/mapstructure's concern, not an unknown key.
func TestCheckMergeValue_ScalarValue_ReturnsNil(t *testing.T) {
	scalar := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "oops"}
	if err := checkMergeValue(scalar, reflect.TypeOf(Config{}), "", "file.yaml"); err != nil {
		t.Errorf("checkMergeValue on a scalar merge value: got %v, want nil", err)
	}
}

// TestCheckMergeValue_Sequence_SkipsNilAndNonMappingThenSucceeds covers the
// "<<: [*a, *b]" sequence form of a merge key, which nothing in this
// package tested before this commit (only the single-mapping "<<: *a" form
// was exercised). Built by hand rather than parsed: a cyclic self-alias
// element (resolves to nil -- skipped, same reasoning as the top-level
// guard above), a scalar element (not a mapping -- skipped, a type
// mismatch elsewhere's concern), and a real mapping element with every key
// known. The loop must skip the first two and succeed on the third.
func TestCheckMergeValue_Sequence_SkipsNilAndNonMappingThenSucceeds(t *testing.T) {
	cyclic := &yaml.Node{Kind: yaml.AliasNode}
	cyclic.Alias = cyclic
	scalar := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "oops"}
	var good yaml.Node
	if err := yaml.Unmarshal([]byte("name: ok\nengine: kv\nbackend: pebble\n"), &good); err != nil {
		t.Fatal(err)
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{cyclic, scalar, good.Content[0]}}
	if err := checkMergeValue(seq, reflect.TypeOf(DatabaseConfig{}), "databases[0]", "file.yaml"); err != nil {
		t.Errorf("checkMergeValue on a sequence with a nil and a non-mapping element: got %v, want nil (both skipped, the mapping element has no unknown key)", err)
	}
}

// TestCheckMergeValue_Sequence_ElementError_Propagates is the sequence
// form's error path: an unknown key inside ANY merged-in mapping is exactly
// as much an unknown key as one in the single-mapping "<<: *a" form, and
// must stop the loop and return it rather than keep checking later
// elements.
func TestCheckMergeValue_Sequence_ElementError_Propagates(t *testing.T) {
	var bad yaml.Node
	if err := yaml.Unmarshal([]byte("name: ok\nbogus_key: true\n"), &bad); err != nil {
		t.Fatal(err)
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{bad.Content[0]}}
	err := checkMergeValue(seq, reflect.TypeOf(DatabaseConfig{}), "databases[0]", "file.yaml")
	var ufe *UnknownFieldError
	if !errors.As(err, &ufe) {
		t.Fatalf("checkMergeValue: got %v (%T), want *UnknownFieldError", err, err)
	}
	if ufe.Path != "databases[0].bogus_key" {
		t.Errorf("Path: got %q, want %q", ufe.Path, "databases[0].bogus_key")
	}
}

// CycleFixtureA and CycleFixtureB inline each other through anonymous
// pointer fields -- legal Go (a pointer's size does not depend on what it
// points to) and the only shape that can reach addYAMLFields' own cycle
// guard, since two struct VALUE types cannot embed each other at all. Both
// must be EXPORTED types: an embedded field's own export-ness follows its
// type's name, not the field's, so embedding an unexported type would make
// addYAMLFields' unexported-field guard skip it before the cycle guard is
// ever reached -- a lowercase first attempt at this fixture proved that the
// hard way. Exercises both named gaps at once: the pointer-dereference loop
// that unwraps an inlined *T before checking whether T is a struct (both
// A's and B's inline field is a pointer), and the visited-cycle guard
// itself (the second time either type is reached, through the other).
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

// TestYamlFieldName_NoTag_UsesLoweredFieldName covers the no-tag,
// non-anonymous branch: a field with no "yaml" tag at all decodes under its
// own lowercased name, same as yaml.v3's own default.
func TestYamlFieldName_NoTag_UsesLoweredFieldName(t *testing.T) {
	f := reflect.TypeOf(struct{ Foo string }{}).Field(0)
	name, inline, ok := yamlFieldName(f)
	if name != "foo" || inline || !ok {
		t.Errorf("yamlFieldName(no tag): got (%q, %v, %v), want (\"foo\", false, true)", name, inline, ok)
	}
}

// TestYamlFieldName_ExactDashTag_OptsOut covers the exact, comma-less "-"
// tag: the field takes no part in decoding at all.
func TestYamlFieldName_ExactDashTag_OptsOut(t *testing.T) {
	f := reflect.TypeOf(struct {
		Foo string `yaml:"-"`
	}{}).Field(0)
	name, inline, ok := yamlFieldName(f)
	if ok {
		t.Errorf("yamlFieldName(yaml:\"-\"): got ok=true (name=%q, inline=%v), want ok=false", name, inline)
	}
}

// TestYamlFieldName_EmptyNameOption_Anonymous_IsInline covers a tag with an
// option but no name (e.g. ",omitempty") on an ANONYMOUS field: with no
// explicit name to decode under, an embedded field still inlines, matching
// yaml.v3's own default for embedding.
func TestYamlFieldName_EmptyNameOption_Anonymous_IsInline(t *testing.T) {
	type leaf struct {
		X string `yaml:"x"`
	}
	f := reflect.TypeOf(struct {
		leaf `yaml:",omitempty"`
	}{}).Field(0)
	name, inline, ok := yamlFieldName(f)
	if name != "" || !inline || !ok {
		t.Errorf("yamlFieldName(anonymous, empty-name tag): got (%q, %v, %v), want (\"\", true, true)", name, inline, ok)
	}
}

// TestYamlFieldName_EmptyNameOption_NotAnonymous_UsesLoweredFieldName is
// the same empty-name tag on a field that is NOT anonymous: with nothing to
// promote and no name, it falls back to the field's own lowercased name,
// exactly like having no tag at all.
func TestYamlFieldName_EmptyNameOption_NotAnonymous_UsesLoweredFieldName(t *testing.T) {
	f := reflect.TypeOf(struct {
		Foo string `yaml:",omitempty"`
	}{}).Field(0)
	name, inline, ok := yamlFieldName(f)
	if name != "foo" || inline || !ok {
		t.Errorf("yamlFieldName(named field, empty-name tag): got (%q, %v, %v), want (\"foo\", false, true)", name, inline, ok)
	}
}
