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
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
)

func TestWriteStreamRecordsTheDigestOfItsBytes(t *testing.T) {
	bw, err := createBundle(filepath.Join(t.TempDir(), "bundle"))
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("streamed line\n"), 10000)
	if err := bw.writeStream("a/b.jsonl", func(w io.Writer) error {
		for off := 0; off < len(data); off += 777 {
			if _, err := w.Write(data[off:min(off+777, len(data))]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("writeStream: %v", err)
	}
	if got, want := bw.files["a/b.jsonl"], digest(data); got != want {
		t.Fatalf("recorded digest %s, want %s", got, want)
	}
	onDisk, err := os.ReadFile(filepath.Join(bw.dir, "a", "b.jsonl"))
	if err != nil || !bytes.Equal(onDisk, data) {
		t.Fatalf("member on disk differs from the streamed bytes (err %v)", err)
	}
	if err := bw.write("c.json", []byte("{}\n")); err != nil || bw.files["c.json"] != digest([]byte("{}\n")) {
		t.Fatalf("write: err %v, digest %s", err, bw.files["c.json"])
	}

	nop := func(io.Writer) error { return nil }
	var be *BundleError
	if err := bw.writeStream("a/b.jsonl", nop); !errors.As(err, &be) {
		t.Fatalf("writeStream over an existing member: %v, want a BundleError", err)
	}
	if err := bw.writeStream("../escape", nop); !errors.As(err, &be) {
		t.Fatalf("writeStream of an invalid path: %v, want a BundleError", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(bw.dir), "escape")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid member written outside the bundle: %v", err)
	}
	fail := errors.New("fill failed")
	if err := bw.writeStream("d.json", func(io.Writer) error { return fail }); !errors.Is(err, fail) {
		t.Fatalf("writeStream with a failing fill: %v, want %v", err, fail)
	}
	if _, ok := bw.files["d.json"]; ok {
		t.Fatal("a member whose fill failed is in the manifest")
	}
}

// TestWriteBundleOfALargeHistoryChecks writes a several-thousand-op history
// through writeBundle, which streams it and keeps one copy, and checks the
// bundle reproduces the run's verdict and manifest digest.
func TestWriteBundleOfALargeHistoryChecks(t *testing.T) {
	src, checkers := cleanBundle(t)
	hf, err := openMember(src, fileHistory)
	if err != nil {
		t.Fatal(err)
	}
	var recorded [2][]byte
	for i, rel := range []string{fileNemesis, fileRun} {
		if recorded[i], err = readMember(src, rel); err != nil {
			t.Fatal(err)
		}
	}
	ev, err := decodeEvidence(src, hf, recorded[0], recorded[1])
	_ = hf.Close()
	if err != nil {
		t.Fatal(err)
	}
	var infos []NodeInfo
	for _, n := range ev.run.Nodes {
		var info NodeInfo
		if err := decodeMember(src, nodeFile(n.Name), &info); err != nil {
			t.Fatal(err)
		}
		infos = append(infos, info)
	}

	h := ev.history
	var maxID uint64
	var end int64
	for _, op := range h {
		maxID, end = max(maxID, op.ID), max(end, op.ReturnNS)
	}
	const extra = 5000
	for i := range extra {
		h = append(h, history.Op{
			ID: maxID + uint64(i) + 1, Client: i % 2, Kind: history.KindGet, Key: "test-0",
			CallNS: end + int64(2*i), ReturnNS: end + int64(2*i) + 1,
			Outcome: history.OutcomeFail, Error: "refused",
		})
	}
	h.Sort()
	var want bytes.Buffer
	if err := history.Encode(&want, h); err != nil {
		t.Fatal(err)
	}

	o := Options{
		Bundle:   filepath.Join(t.TempDir(), "bundle"),
		Checkers: checkers,
		Harness:  Harness{Version: "test", Commit: "test", Go: "test"},
	}
	bw, err := createBundle(o.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	res, err := o.writeBundle(bw, h, ev.events, ev.run, infos)
	if err != nil {
		t.Fatalf("writeBundle: %v", err)
	}
	got, err := readMember(o.Bundle, fileHistory)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("%s differs from history.Encode of the history (%d bytes, want %d)", fileHistory, len(got), want.Len())
	}
	timeline, err := readMember(o.Bundle, fileTimeline)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := encodeLines(buildTimeline(h, ev.events))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(timeline, lines) {
		t.Fatalf("streamed %s differs from its encodeLines spelling", fileTimeline)
	}

	chk, err := Check(o.Bundle, CheckOptions{Checkers: checkers})
	if err != nil {
		t.Fatalf("Check of the large bundle: %v", err)
	}
	if chk.ManifestSHA256 != res.ManifestSHA256 {
		t.Fatalf("check manifest digest %s, writeBundle printed %s", chk.ManifestSHA256, res.ManifestSHA256)
	}
	if chk.Verdict.Exit != res.Verdict.Exit || chk.Verdict.Outcome != res.Verdict.Outcome {
		t.Fatalf("check verdict %s/%d, writeBundle verdict %s/%d", chk.Verdict.Outcome, chk.Verdict.Exit, res.Verdict.Outcome, res.Verdict.Exit)
	}
}
