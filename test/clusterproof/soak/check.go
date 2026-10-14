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
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
)

// maxEventLine bounds one line of nemesis.jsonl; an event is small, and the
// schedule event is the largest.
const maxEventLine = 4 << 20

// CheckOptions configures a check of a bundle.
type CheckOptions struct {
	// Checkers are the history checkers to run beside the ledger. They must
	// be the run's checkers: the check derives verdict.json again and a
	// different set derives a different one.
	Checkers []history.Checker
}

// CheckResult is what a check of a bundle established.
type CheckResult struct {
	// Verdict is the verdict derived again from the bundle's evidence. It is
	// byte-identical to the bundle's verdict.json.
	Verdict Verdict
	// ManifestSHA256 is the manifest's digest; it matches the one the run
	// printed exactly when the bundle is the one the run wrote.
	ManifestSHA256 string
}

// Check verifies the bundle in dir with no cluster: every file against the
// manifest, no file missing or extra, the recorded evidence well formed, and
// the derived files -- timeline, timings and verdict -- derived again from it
// byte for byte. Any failure is a *BundleError: the bundle supports no
// verdict.
func Check(dir string, opts CheckOptions) (CheckResult, error) {
	manifestData, err := readMember(dir, fileManifest)
	if err != nil {
		return CheckResult{}, err
	}
	var m Manifest
	if err := decodeStrict(manifestData, &m); err != nil {
		return CheckResult{}, &BundleError{Path: dir, Reason: "malformed " + fileManifest, Err: err}
	}
	if m.Format != bundleFormat {
		return CheckResult{}, &BundleError{Path: dir, Reason: "unsupported bundle format " + strconv.Itoa(m.Format)}
	}
	if err := verifyFiles(dir, m.Files); err != nil {
		return CheckResult{}, err
	}

	var recorded [3][]byte
	for i, rel := range []string{fileHistory, fileNemesis, fileRun} {
		if recorded[i], err = readMember(dir, rel); err != nil {
			return CheckResult{}, err
		}
	}
	ev, err := decodeEvidence(dir, recorded[0], recorded[1], recorded[2])
	if err != nil {
		return CheckResult{}, err
	}
	if err := expectMembers(dir, m.Files, ev.run.Nodes); err != nil {
		return CheckResult{}, err
	}
	for _, n := range ev.run.Nodes {
		var info NodeInfo
		if err := decodeMember(dir, nodeFile(n.Name), &info); err != nil {
			return CheckResult{}, err
		}
	}
	ev.checkers = opts.Checkers
	v, d, err := derive(ev)
	if err != nil {
		return CheckResult{}, &BundleError{Path: dir, Reason: "cannot derive the verdict", Err: err}
	}
	for _, f := range []struct {
		name string
		want []byte
	}{
		{fileTimeline, d.timeline},
		{fileTimings, d.timings},
		{fileVerdict, d.verdict},
	} {
		got, err := readMember(dir, f.name)
		if err != nil {
			return CheckResult{}, err
		}
		if !bytes.Equal(got, f.want) {
			return CheckResult{}, &BundleError{Path: dir, Reason: f.name + " does not follow from the evidence: " +
				"the bundle was altered and its manifest rewritten, or the check runs other checkers than the run did"}
		}
	}
	return CheckResult{Verdict: v, ManifestSHA256: digest(manifestData)}, nil
}

// verifyFiles walks dir and compares it with the manifest: every regular
// file listed and matching its digest, nothing else present.
func verifyFiles(dir string, files map[string]string) error {
	for rel := range files {
		if !validMember(rel) || rel == fileManifest {
			return &BundleError{Path: dir, Reason: "manifest lists an invalid member " + strconv.Quote(rel)}
		}
	}
	seen := make(map[string]bool, len(files))
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			return &BundleError{Path: dir, Reason: rel + " is not a regular file"}
		case rel == fileManifest:
			return nil
		}
		want, ok := files[rel]
		if !ok {
			return &BundleError{Path: dir, Reason: rel + " is not in the manifest"}
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return &BundleError{Path: dir, Reason: "cannot read " + rel, Err: err}
		}
		if digest(data) != want {
			return &BundleError{Path: dir, Reason: rel + " does not match its manifest checksum"}
		}
		seen[rel] = true
		return nil
	})
	if err != nil {
		if be, ok := errors.AsType[*BundleError](err); ok {
			return be
		}
		return &BundleError{Path: dir, Reason: "cannot walk the bundle", Err: err}
	}
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		if !seen[rel] {
			return &BundleError{Path: dir, Reason: rel + " is in the manifest and missing from the bundle"}
		}
	}
	return nil
}

// expectMembers checks that the manifest lists exactly the files a bundle of
// these nodes has.
func expectMembers(dir string, files map[string]string, nodes []Node) error {
	want := []string{fileHistory, fileNemesis, fileRun, fileTimeline, fileTimings, fileVerdict}
	for _, n := range nodes {
		if !validNodeName(n.Name) {
			return &BundleError{Path: dir, Reason: fileRun + " names an invalid node " + strconv.Quote(n.Name)}
		}
		want = append(want, nodeFile(n.Name))
	}
	for _, rel := range want {
		if _, ok := files[rel]; !ok {
			return &BundleError{Path: dir, Reason: "manifest lacks " + rel}
		}
	}
	if len(files) != len(want) {
		for _, rel := range slices.Sorted(maps.Keys(files)) {
			if !slices.Contains(want, rel) {
				return &BundleError{Path: dir, Reason: "manifest lists " + rel + ", which a bundle does not have"}
			}
		}
	}
	return nil
}

// readMember reads one regular file of the bundle.
func readMember(dir, rel string) ([]byte, error) {
	p := filepath.Join(dir, filepath.FromSlash(rel))
	st, err := os.Lstat(p)
	if err != nil {
		return nil, &BundleError{Path: dir, Reason: "cannot read " + rel, Err: err}
	}
	if !st.Mode().IsRegular() {
		return nil, &BundleError{Path: dir, Reason: rel + " is not a regular file"}
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, &BundleError{Path: dir, Reason: "cannot read " + rel, Err: err}
	}
	return data, nil
}

// decodeMember strictly decodes one JSON file of the bundle.
func decodeMember(dir, rel string, v any) error {
	data, err := readMember(dir, rel)
	if err != nil {
		return err
	}
	if err := decodeStrict(data, v); err != nil {
		return &BundleError{Path: dir, Reason: "malformed " + rel, Err: err}
	}
	return nil
}

// decodeEvents decodes nemesis.jsonl.
func decodeEvents(dir string, data []byte) ([]EventRecord, error) {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), maxEventLine)
	var events []EventRecord
	for line := 1; sc.Scan(); line++ {
		var e EventRecord
		if err := decodeStrict(sc.Bytes(), &e); err != nil {
			return nil, &BundleError{Path: dir, Reason: "malformed " + fileNemesis + " line " + strconv.Itoa(line), Err: err}
		}
		events = append(events, e)
	}
	if err := sc.Err(); err != nil {
		return nil, &BundleError{Path: dir, Reason: "cannot read " + fileNemesis, Err: err}
	}
	return events, nil
}

// decodeStrict decodes exactly one JSON value with no unknown field.
func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errTrailingData
	}
	return nil
}

// errTrailingData reports a JSON file holding more than one value.
var errTrailingData = errors.New("soak: trailing data after the JSON value")
