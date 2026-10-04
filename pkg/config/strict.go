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
	"reflect"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// validateKnownFields decodes data against the Config schema and refuses any
// key the schema does not recognize, naming the key's full path and the line
// it appeared on in file. It is a pre-pass over Load's own Viper-based merge:
// Viper's mapstructure decode step silently drops a key that matches no
// struct field, which is how a stale or misspelled key (go-qrdb's own
// motivating case was the documented-but-wrong "databases[].protocol.etcd")
// is accepted and then quietly has no effect. This walks the raw YAML
// document tree against the Config struct's own "yaml" tags, so the known-key
// set is always exactly what Load actually decodes into — never a second,
// separately maintained list that could drift from it.
//
// A build-tag-gated feature's key (e.g. "protocols.mysql" on a binary built
// without qrdb_mysql) is still a KNOWN field here: pkg/config declares every
// field unconditionally regardless of build tags, and the capability gate
// that refuses an unbuilt feature is the separate, deliberately
// build-independent Config.ValidateCapabilities.
func validateKnownFields(path string, data []byte) error {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return &LoadError{Path: path, Err: err}
	}
	if len(root.Content) == 0 {
		// An empty file. Nothing to check; Load proceeds on defaults alone.
		return nil
	}
	return checkNode(root.Content[0], reflect.TypeOf(Config{}), "", path)
}

// checkNode validates node against the schema described by t, recursing into
// struct fields and slice elements. Maps are deliberately left unchecked:
// every map-typed field in Config (e.g. ObjstoreConfig.Settings) is an
// intentionally open, provider-defined bag whose keys are not part of this
// schema.
//
// node is resolved through any YAML alias first (e.g. "tls: *common_tls"),
// so a typo inside an aliased block is still caught rather than skipped
// because the node's own Kind looked like a plain alias rather than a
// mapping or sequence.
func checkNode(node *yaml.Node, t reflect.Type, path, file string) error {
	node = resolveAlias(node)
	if node == nil {
		return nil
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		return checkStructNode(node, t, path, file)
	case reflect.Slice, reflect.Array:
		return checkSliceNode(node, t.Elem(), path, file)
	default:
		// Scalars, maps and everything else carry no further schema of their
		// own to check against; a type mismatch here is Viper/mapstructure's
		// concern, not an unknown-key concern.
		return nil
	}
}

// resolveAlias follows node through any chain of YAML aliases (an alias may
// itself point at another alias) and returns the node it ultimately
// resolves to, or the node itself when it is not an alias. It returns nil
// rather than hang when the chain cycles back on itself -- not reachable
// through ordinary YAML text (an alias can only name an anchor that
// appears earlier in the document), but a defensive floor under anything
// that constructs a *yaml.Node graph by hand.
func resolveAlias(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	seen := make(map[*yaml.Node]bool)
	for node != nil && node.Kind == yaml.AliasNode {
		if seen[node] {
			return nil
		}
		seen[node] = true
		node = node.Alias
	}
	return node
}

// mergeKeyTag is the YAML type yaml.v3 assigns to the "<<" mapping key
// (the language-independent merge-key type, tag:yaml.org,2002:merge).
const mergeKeyTag = "!!merge"

// isMergeKey reports whether keyNode is a YAML merge key ("<<"). Checking
// the resolved tag rather than the literal text handles a merge key
// written with an explicit "!!merge" tag of its own; checking the text
// too covers a node tree built by hand without going through the YAML
// resolver.
func isMergeKey(keyNode *yaml.Node) bool {
	return keyNode.Tag == mergeKeyTag || keyNode.Value == "<<"
}

// checkStructNode refuses the first key under node that does not name a
// field of struct type t.
//
// A "<<" merge key ("<<: *defaults" or "<<: [*a, *b]") is standard YAML
// that both yaml.v3 and Viper honour: its value is merged into the SAME
// mapping, at the SAME level, as every sibling key, so the mapping(s) it
// names are validated against this same (t, path) rather than treated as
// an unknown key or a nested field.
func checkStructNode(node *yaml.Node, t reflect.Type, path, file string) error {
	if node.Kind != yaml.MappingNode {
		// Not a mapping (null, a scalar where a section was expected, ...).
		// That is a type mismatch Validate/mapstructure will report; it is
		// not an unknown key.
		return nil
	}
	fields := structYAMLFields(t)
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valNode := node.Content[i+1]
		if isMergeKey(keyNode) {
			if err := checkMergeValue(valNode, t, path, file); err != nil {
				return err
			}
			continue
		}
		field, ok := fields[keyNode.Value]
		if !ok {
			return &UnknownFieldError{File: file, Path: joinField(path, keyNode.Value), Line: keyNode.Line}
		}
		if err := checkNode(valNode, field.Type, joinField(path, keyNode.Value), file); err != nil {
			return err
		}
	}
	return nil
}

// checkMergeValue validates the value of a "<<" merge key: either a single
// mapping ("<<: *defaults") or a sequence of mappings ("<<: [*a, *b]"),
// each resolved through any alias first. Every merged-in mapping is
// checked against the same struct type and path as the mapping it merges
// into -- that is what "merge" means in YAML, so a typo inside it is
// exactly as much an unknown key as a typo written directly.
func checkMergeValue(node *yaml.Node, t reflect.Type, path, file string) error {
	node = resolveAlias(node)
	if node == nil {
		return nil
	}
	switch node.Kind {
	case yaml.MappingNode:
		return checkStructNode(node, t, path, file)
	case yaml.SequenceNode:
		for _, elem := range node.Content {
			elem = resolveAlias(elem)
			if elem == nil {
				continue
			}
			if elem.Kind != yaml.MappingNode {
				continue
			}
			if err := checkStructNode(elem, t, path, file); err != nil {
				return err
			}
		}
		return nil
	default:
		return nil
	}
}

// checkSliceNode validates every element of a YAML sequence node against
// elemType, naming the element's index in the path (e.g. "databases[2]").
func checkSliceNode(node *yaml.Node, elemType reflect.Type, path, file string) error {
	if node.Kind != yaml.SequenceNode {
		return nil
	}
	for i, elem := range node.Content {
		if err := checkNode(elem, elemType, path+"["+strconv.Itoa(i)+"]", file); err != nil {
			return err
		}
	}
	return nil
}

// joinField appends a field name to a dotted path, e.g.
// joinField("databases[2]", "protocol") == "databases[2].protocol".
func joinField(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

// structYAMLFields returns t's own and inlined fields keyed by the name
// they decode from -- the same names Viper's mapstructure decode step
// (TagName: "yaml") resolves against, so this is exactly the known-key set
// Load actually honors.
//
// Config has no inline or anonymous-embedded field today, but a future one
// would otherwise make every one of its promoted fields read as "unknown":
// an inlined struct's keys live at the SAME level as its parent's, exactly
// like a "<<" merge's do, so they are flattened into the same map rather
// than nested under the embedding field's own name. visited guards against
// an embedding cycle between struct types (never legitimate Go, but cheap
// to make non-fatal rather than infinite).
func structYAMLFields(t reflect.Type) map[string]reflect.StructField {
	fields := make(map[string]reflect.StructField, t.NumField())
	addYAMLFields(t, fields, map[reflect.Type]bool{})
	return fields
}

// addYAMLFields adds t's fields into fields, recursing into any inline or
// anonymous-embedded struct field so its keys land in the same map as
// everything else at this level.
func addYAMLFields(t reflect.Type, fields map[string]reflect.StructField, visited map[reflect.Type]bool) {
	if visited[t] {
		return
	}
	visited[t] = true
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			// Unexported; not decodable and not a valid YAML key for this
			// struct.
			continue
		}
		name, inline, ok := yamlFieldName(f)
		if !ok {
			continue
		}
		if inline {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				addYAMLFields(ft, fields, visited)
			}
			continue
		}
		fields[name] = f
	}
}

// yamlFieldName returns the YAML key f decodes from, whether f is inlined
// into its parent rather than decoding under a key of its own, and whether
// f participates in YAML decoding at all ("yaml:\"-\"" opts out).
//
// Mirrors yaml.v3's own field rules: an explicit ",inline" option inlines
// a struct or map field; an anonymous (embedded) field inlines the same
// way UNLESS its tag gives it an explicit name, in which case it decodes
// under that name like any other field rather than being promoted.
func yamlFieldName(f reflect.StructField) (name string, inline bool, ok bool) {
	tag, has := f.Tag.Lookup("yaml")
	if !has {
		if f.Anonymous {
			return "", true, true
		}
		return strings.ToLower(f.Name), false, true
	}
	if tag == "-" {
		// The exact, comma-less tag "-" opts out; "-," names the field "-"
		// literally and falls through to the general case below like any
		// other name, matching yaml.v3's own distinction.
		return "", false, false
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	for _, opt := range parts[1:] {
		if opt == "inline" {
			return "", true, true
		}
	}
	if name == "" {
		if f.Anonymous {
			return "", true, true
		}
		name = strings.ToLower(f.Name)
	}
	return name, false, true
}
