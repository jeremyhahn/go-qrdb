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
func checkNode(node *yaml.Node, t reflect.Type, path, file string) error {
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

// checkStructNode refuses the first key under node that does not name a
// field of struct type t.
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

// structYAMLFields returns t's exported fields keyed by the name their
// "yaml" struct tag decodes from -- the same name Viper's mapstructure
// decode step (TagName: "yaml") resolves against, so this is exactly the
// known-key set Load actually honors.
func structYAMLFields(t reflect.Type) map[string]reflect.StructField {
	fields := make(map[string]reflect.StructField, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			// Unexported; not decodable and not a valid YAML key for this
			// struct.
			continue
		}
		name, ok := yamlFieldName(f)
		if !ok {
			continue
		}
		fields[name] = f
	}
	return fields
}

// yamlFieldName returns the YAML key f decodes from, and whether f
// participates in YAML decoding at all ("yaml:\"-\"" opts out).
func yamlFieldName(f reflect.StructField) (name string, ok bool) {
	tag, has := f.Tag.Lookup("yaml")
	if !has {
		return strings.ToLower(f.Name), true
	}
	if tag == "-" {
		return "", false
	}
	name = tag
	if idx := strings.IndexByte(tag, ','); idx >= 0 {
		name = tag[:idx]
	}
	if name == "" {
		name = strings.ToLower(f.Name)
	}
	return name, true
}
