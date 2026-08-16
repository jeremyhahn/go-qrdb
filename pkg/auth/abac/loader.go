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

package abac

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadDir loads and validates every policy document from the YAML files
// (*.yaml, *.yml) directly inside dir. Sub-directories are not descended
// into. Each file may contain multiple policy documents separated by
// "---". Files are read in lexical order so load results are
// deterministic. An unreadable directory, an unparsable file, or an
// invalid policy fails the whole load (fail closed).
func LoadDir(dir string) ([]*Policy, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, &PolicyDirError{Dir: dir, Err: err}
	}

	var policies []*Policy
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		filePolicies, err := LoadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		policies = append(policies, filePolicies...)
	}
	return policies, nil
}

// LoadFile loads and validates the policy documents contained in a single
// YAML file. The file may contain multiple documents separated by "---";
// empty documents are skipped.
func LoadFile(path string) ([]*Policy, error) {
	f, err := os.Open(path) // #nosec G304 -- path is operator-configured policy dir content
	if err != nil {
		return nil, &PolicyFileError{Path: path, Err: err}
	}
	defer func() { _ = f.Close() }()

	var policies []*Policy
	dec := yaml.NewDecoder(f)
	for {
		var policy Policy
		if err := dec.Decode(&policy); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, &PolicyFileError{Path: path, Err: err}
		}
		// Skip empty documents (e.g. a trailing "---").
		if policy.APIVersion == "" && policy.Kind == "" && policy.Metadata.Name == "" {
			continue
		}
		if err := policy.Validate(); err != nil {
			return nil, &PolicyFileError{Path: path, Err: err}
		}
		policies = append(policies, &policy)
	}
	return policies, nil
}
