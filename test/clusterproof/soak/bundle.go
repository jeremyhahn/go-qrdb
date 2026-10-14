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
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/nemesis"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/workload"
)

// bundleFormat versions every file the soak writes. A check refuses a bundle
// of another format rather than misreading it.
const bundleFormat = 2

// The files of an evidence bundle. history.jsonl, nemesis.jsonl, run.json and
// the node files are the recorded evidence; timeline.jsonl, timings.json and
// verdict.json are derived from it, and the check derives them again and
// compares bytes. manifest.json holds the SHA-256 of every other file.
const (
	fileManifest = "manifest.json"
	fileHistory  = "history.jsonl"
	fileNemesis  = "nemesis.jsonl"
	fileRun      = "run.json"
	fileTimeline = "timeline.jsonl"
	fileTimings  = "timings.json"
	fileVerdict  = "verdict.json"
	dirNodes     = "nodes"
	fileNode     = "node.json"
)

// Harness identifies the build of the harness that wrote a bundle.
type Harness struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Go      string `json:"go"`
}

// Declared is what the operator states about the cluster under test that the
// harness cannot observe through the client surface: the read mode, which
// for a gate run is raft.lease_read at its default, true. The bundle records
// what the operator declared.
type Declared struct {
	ReadMode string `json:"read_mode"`
}

// undeclared is what a Declared field says when the operator stated nothing.
const undeclared = "undeclared"

// Manifest is manifest.json: who wrote the bundle, and the SHA-256 of every
// other file in it, by slash-separated path relative to the bundle.
type Manifest struct {
	Format  int               `json:"format"`
	Created string            `json:"created"`
	Harness Harness           `json:"harness"`
	Files   map[string]string `json:"files"`
}

// EventRecord is one nemesis event as nemesis.jsonl holds it: the event, and
// its time on the run's clock, the clock the history uses.
type EventRecord struct {
	TimeNS int64 `json:"time_ns"`
	nemesis.Event
}

// WorkloadStats is the workload's own count of what it recorded.
type WorkloadStats struct {
	OK            uint64 `json:"ok"`
	Fail          uint64 `json:"fail"`
	Indeterminate uint64 `json:"indeterminate"`
	DialFailures  uint64 `json:"dial_failures"`
}

// RunRecord is run.json: the run's inputs, and the facts the run observed
// that are not in the history -- the cluster's answers after the run, and
// what went wrong with the run itself.
type RunRecord struct {
	Format   int      `json:"format"`
	Started  string   `json:"started"`
	Ended    string   `json:"ended"`
	Nodes    []Node   `json:"nodes"`
	Duration string   `json:"duration"`
	Nemesis  []string `json:"nemesis"`
	Seed     int64    `json:"seed"`
	// Seal is the node seal type the cluster ran under (PROOF_SEAL).
	Seal     string           `json:"seal"`
	Schedule nemesis.Schedule `json:"schedule"`
	// Workload is the workload configuration with its credentials removed.
	Workload workload.Config `json:"workload"`
	Declared Declared        `json:"declared"`
	Stats    WorkloadStats   `json:"stats"`
	// Replicas is the E3 observation, Liveness the E4 one.
	Replicas Observation `json:"replicas"`
	Liveness Observation `json:"liveness"`
	Status   RunStatus   `json:"status"`
}

// TimelineEntry is one line of timeline.jsonl: the history's calls and
// returns and the nemesis events on the one clock they share.
type TimelineEntry struct {
	TimeNS  int64            `json:"time_ns"`
	Source  string           `json:"source"`
	Type    string           `json:"type"`
	Op      *TimelineOp      `json:"op,omitempty"`
	Nemesis *TimelineNemesis `json:"nemesis,omitempty"`
}

// TimelineOp names the operation behind a client entry; history.jsonl holds
// the rest of it.
type TimelineOp struct {
	ID      uint64          `json:"id"`
	Client  int             `json:"client"`
	Kind    history.Kind    `json:"kind"`
	Key     string          `json:"key"`
	Outcome history.Outcome `json:"outcome,omitempty"`
}

// TimelineNemesis is a nemesis entry's event.
type TimelineNemesis struct {
	Phase   nemesis.Phase `json:"phase"`
	Window  int           `json:"window"`
	Name    string        `json:"name,omitempty"`
	Targets []string      `json:"targets,omitempty"`
	Error   string        `json:"error,omitempty"`
}

// The sources and client types of a timeline entry.
const (
	sourceClient  = "client"
	sourceNemesis = "nemesis"
	typeInvoke    = "invoke"
	typeComplete  = "complete"
)

// buildTimeline merges the history and the events in time order. Ties keep a
// fixed order -- events first, then calls, then returns, each by ID or
// position -- so the same evidence always gives the same bytes.
func buildTimeline(h history.History, events []EventRecord) []TimelineEntry {
	ops := slices.Clone(h)
	slices.SortFunc(ops, func(a, b history.Op) int { return cmp.Compare(a.ID, b.ID) })
	out := make([]TimelineEntry, 0, len(events)+2*len(ops))
	for _, e := range events {
		out = append(out, TimelineEntry{
			TimeNS: e.TimeNS, Source: sourceNemesis, Type: string(e.Op),
			Nemesis: &TimelineNemesis{Phase: e.Phase, Window: e.Window, Name: e.Nemesis, Targets: e.Targets, Error: e.Error},
		})
	}
	for _, op := range ops {
		out = append(out, TimelineEntry{
			TimeNS: op.CallNS, Source: sourceClient, Type: typeInvoke,
			Op: &TimelineOp{ID: op.ID, Client: op.Client, Kind: op.Kind, Key: op.Key},
		})
	}
	for _, op := range ops {
		out = append(out, TimelineEntry{
			TimeNS: op.ReturnNS, Source: sourceClient, Type: typeComplete,
			Op: &TimelineOp{ID: op.ID, Client: op.Client, Kind: op.Kind, Key: op.Key, Outcome: op.Outcome},
		})
	}
	slices.SortStableFunc(out, func(a, b TimelineEntry) int { return cmp.Compare(a.TimeNS, b.TimeNS) })
	return out
}

// encodeJSON is the one spelling of a JSON file in a bundle.
func encodeJSON(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// encodeLines is the one spelling of a JSON-lines file in a bundle.
func encodeLines[T any](items []T) ([]byte, error) {
	var buf bytes.Buffer
	for _, it := range items {
		b, err := json.Marshal(it)
		if err != nil {
			return nil, err
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// encodeHistory is history.jsonl's bytes.
func encodeHistory(h history.History) ([]byte, error) {
	var buf bytes.Buffer
	if err := history.Encode(&buf, h); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// derived is the bytes of the files derived from the evidence.
type derived struct {
	timeline, timings, verdict []byte
}

// derive judges the evidence and spells the derived files.
func derive(ev evidence) (Verdict, derived, error) {
	v := judge(ev)
	var d derived
	var err error
	if d.timeline, err = encodeLines(buildTimeline(ev.history, ev.events)); err != nil {
		return v, d, err
	}
	if d.timings, err = encodeJSON(ev.timings); err != nil {
		return v, d, err
	}
	d.verdict, err = encodeJSON(v)
	return v, d, err
}

// bundleWriter writes a bundle's files once each and remembers their
// digests for the manifest.
type bundleWriter struct {
	dir   string
	files map[string]string
}

// createBundle creates dir, which must not exist yet: a bundle is never
// written over another.
func createBundle(dir string) (*bundleWriter, error) {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return nil, &BundleError{Path: dir, Reason: "cannot create the parent directory", Err: err}
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		return nil, &BundleError{Path: dir, Reason: "cannot create the bundle directory; it must not exist yet", Err: err}
	}
	return &bundleWriter{dir: dir, files: make(map[string]string)}, nil
}

// write creates rel, a slash-separated path inside the bundle.
func (b *bundleWriter) write(rel string, data []byte) error {
	if !validMember(rel) {
		return &BundleError{Path: b.dir, Reason: "invalid member path " + rel}
	}
	p := filepath.Join(b.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return &BundleError{Path: p, Reason: "cannot create directory", Err: err}
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return &BundleError{Path: p, Reason: "cannot create file", Err: err}
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		return &BundleError{Path: p, Reason: "cannot write file", Err: cmp.Or(werr, cerr)}
	}
	b.files[rel] = digest(data)
	return nil
}

// finish writes the manifest last, over every file written before it, and
// returns the manifest's own digest: the one value that pins the bundle.
func (b *bundleWriter) finish(m Manifest) (string, error) {
	m.Files = b.files
	data, err := encodeJSON(m)
	if err != nil {
		return "", &BundleError{Path: b.dir, Reason: "cannot encode the manifest", Err: err}
	}
	if err := b.write(fileManifest, data); err != nil {
		return "", err
	}
	return digest(data), nil
}

// validMember reports whether rel is a clean relative path that stays inside
// the bundle.
func validMember(rel string) bool {
	return rel != "" && rel != "." && !strings.Contains(rel, `\`) && !path.IsAbs(rel) &&
		path.Clean(rel) == rel && rel != ".." && !strings.HasPrefix(rel, "../")
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// nodeFile is the bundle path of a node's file.
func nodeFile(name string) string { return dirNodes + "/" + name + "/" + fileNode }
