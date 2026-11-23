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

package buildgates

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// goCodecGapSites are the only non-test files that may import a
// serialization library directly (issue #1646: go-codec owns the formats).
// Each needs an API go-codec does not expose yet; docs/plans/1646.md names
// the go-codec feature that would remove the entry. The list only shrinks.
var goCodecGapSites = map[string]string{
	"pkg/config/strict.go":                 "walks a yaml.Node tree for known-field checks; go-codec yaml exposes no Node",
	"pkg/auth/abac/loader.go":              "reads a multi-document stream on one yaml.Decoder; go-codec Decode opens a new decoder per call",
	"cmd/qrdb/abac_policy.go":              "reads a multi-document stream on one yaml.Decoder; go-codec Decode opens a new decoder per call",
	"pkg/secret/secret_value_codec.go":     "implements yaml.Marshaler/Unmarshaler hooks that take *yaml.Node",
	"pkg/dao/secret_bytes_msgpack.go":      "implements msgpack CustomEncoder/CustomDecoder and uses the GetEncoder pool",
	"pkg/dao/codec_cbor.go":                "builds fxamacker DecOptions/EncOptions for go-codec cbor.NewWithOptions",
	"test/tools/nightlysignal/workflow.go": "walks a yaml.Node tree of a workflow file; go-codec yaml exposes no Node",
}

// directFormatImport reports whether path is a serialization library that
// go-codec wraps.
func directFormatImport(path string) bool {
	for _, lib := range []string{
		"gopkg.in/yaml.v3",
		"go.yaml.in/yaml/v3",
		"github.com/vmihailenco/msgpack/v5",
		"github.com/fxamacker/cbor/v2",
	} {
		if path == lib || strings.HasPrefix(path, lib+"/") {
			return true
		}
	}
	return strings.HasPrefix(path, "go.mongodb.org/mongo-driver") &&
		(strings.HasSuffix(path, "/bson") || strings.Contains(path, "/bson/"))
}

// goCodecTestGapSites are the only _test.go files that may import a
// serialization library directly. Each walks yaml.Node trees (or exercises
// the msgpack hooks of a goCodecGapSites file) through an API go-codec does
// not expose yet; docs/plans/1646.md names the go-codec feature that would
// remove the entry. The list only shrinks.
var goCodecTestGapSites = map[string]string{
	"pkg/config/strict_yaml_features_test.go":           "builds and walks yaml.Node trees for the strict decoder; go-codec yaml exposes no Node",
	"pkg/dao/secret_bytes_discard_test.go":              "asserts a direct msgpack decode bypasses the gap site's CustomDecoder tracking; go-codec msgpack exposes no hooks",
	"pkg/dao/secret_bytes_msgpack_test.go":              "exercises the msgpack CustomEncoder/CustomDecoder hooks of pkg/dao/secret_bytes_msgpack.go; go-codec msgpack exposes no hooks",
	"test/buildgates/action_pins_test.go":               "walks a yaml.Node tree of the workflow files; go-codec yaml exposes no Node",
	"test/buildgates/ci_runners_test.go":                "walks a yaml.Node tree of the workflow files; go-codec yaml exposes no Node",
	"test/buildgates/ci_tiers_test.go":                  "walks a yaml.Node tree of the workflow files; go-codec yaml exposes no Node",
	"test/buildgates/compose_tmp_test.go":               "walks a yaml.Node tree of the compose files; go-codec yaml exposes no Node",
	"test/buildgates/coverage_legs_test.go":             "walks a yaml.Node tree of the workflow files; go-codec yaml exposes no Node",
	"test/buildgates/devcontainer_run_services_test.go": "walks a yaml.Node tree of the compose files; go-codec yaml exposes no Node",
	"test/buildgates/feature_jobs_gate_test.go":         "walks a yaml.Node tree of the workflow files; go-codec yaml exposes no Node",
	"test/buildgates/fips_legs_test.go":                 "walks a yaml.Node tree of the workflow files; go-codec yaml exposes no Node",
	"test/buildgates/integration_image_tag_test.go":     "walks a yaml.Node tree of the workflow files; go-codec yaml exposes no Node",
	"test/buildgates/release_workflow_test.go":          "walks a yaml.Node tree of the workflow files; go-codec yaml exposes no Node",
}

// formatImportsByFile maps each Go file under pkg, cmd, sdk/go and test
// (repository-relative, slash-separated) to the format libraries it imports
// directly: non-test files when tests is false, _test.go files when it is
// true. Files that import none are absent.
func formatImportsByFile(t *testing.T, tests bool) map[string][]string {
	t.Helper()
	root := repoRoot(t)
	found := map[string][]string{}
	files := 0
	fset := token.NewFileSet()
	for _, dir := range []string{"pkg", "cmd", "sdk/go", "test"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if name := d.Name(); name == "testdata" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") != tests || strings.HasSuffix(path, ".pb.go") {
				return nil
			}
			files++
			f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imp := range f.Imports {
				p, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					return err
				}
				if directFormatImport(p) {
					rel, _ := filepath.Rel(root, path)
					rel = filepath.ToSlash(rel)
					found[rel] = append(found[rel], p)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	kind := "non-test"
	if tests {
		kind = "test"
	}
	if files < minImportFiles {
		t.Fatalf("found %d %s Go files, want at least %d: the walk is not looking where the code is", files, kind, minImportFiles)
	}
	return found
}

func TestDirectFormatImports_OnlyAtGoCodecGapSites(t *testing.T) {
	found := formatImportsByFile(t, false)
	var bad []string
	for file, libs := range found {
		if _, ok := goCodecGapSites[file]; ok {
			continue
		}
		bad = append(bad, file+": imports "+strings.Join(libs, ", ")+
			" directly; use github.com/jeremyhahn/go-codec, or add the file to goCodecGapSites with the go-codec API it lacks")
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Error(b)
	}
}

func TestDirectFormatImports_AllowlistHasNoStaleEntry(t *testing.T) {
	found := formatImportsByFile(t, false)
	var stale []string
	for file := range goCodecGapSites {
		if _, ok := found[file]; !ok {
			stale = append(stale, file+": allowlisted in goCodecGapSites but imports no format library; delete the entry")
		}
	}
	sort.Strings(stale)
	for _, s := range stale {
		t.Error(s)
	}
}

func TestDirectFormatImports_TestFilesOnlyAtGoCodecGapSites(t *testing.T) {
	found := formatImportsByFile(t, true)
	var bad []string
	for file, libs := range found {
		if _, ok := goCodecTestGapSites[file]; ok {
			continue
		}
		bad = append(bad, file+": imports "+strings.Join(libs, ", ")+
			" directly; use github.com/jeremyhahn/go-codec, or add the file to goCodecTestGapSites with the go-codec API it lacks")
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Error(b)
	}
}

func TestDirectFormatImports_TestAllowlistHasNoStaleEntry(t *testing.T) {
	found := formatImportsByFile(t, true)
	var stale []string
	for file := range goCodecTestGapSites {
		if _, ok := found[file]; !ok {
			stale = append(stale, file+": allowlisted in goCodecTestGapSites but imports no format library; delete the entry")
		}
	}
	sort.Strings(stale)
	for _, s := range stale {
		t.Error(s)
	}
}
