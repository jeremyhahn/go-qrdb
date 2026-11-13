package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	keptA   = ".\tinternal\tfunc\texample.com/m/pkg.a"
	keptB   = ".\tinternal\tfunc\texample.com/m/pkg.b"
	leaving = ".\tinternal\tfunc\texample.com/m/pkg.gone"
	plainE  = ".\texported\tfunc\texample.com/m/pkg.Exported"
	fresh   = ".\tinternal\tfunc\texample.com/m/pkg.fresh"
	other   = "sdk/go\tinternal\tfunc\texample.com/m/sdk.other"
)

func sampleBaseline() string {
	return baselineHeader + plainE + "\n" + reasonsPreamble +
		"#\n" + reasonPrefix + "helpers of the exported pkg API.\n" + keptB + "\n" + keptA + "\n" +
		"#\n" + reasonPrefix + "only test code reaches it.\n" + leaving + "\n" +
		"#\n" + reasonPrefix + "a generic at its origin.\n" + other + "\n"
}

func TestParseBaselineGroupsEntriesUnderTheirReason(t *testing.T) {
	b := parseBaseline(sampleBaseline())
	if len(b.plain) != 1 || b.plain[0] != plainE {
		t.Fatalf("plain = %q, want [%q]", b.plain, plainE)
	}
	if len(b.groups) != 3 {
		t.Fatalf("groups = %d, want 3", len(b.groups))
	}
	if g := b.groups[0]; g.reason != "helpers of the exported pkg API." || len(g.entries) != 2 {
		t.Fatalf("group 0 = %+v", g)
	}
}

func TestParseBaselineNonReasonCommentEndsTheGroup(t *testing.T) {
	b := parseBaseline(reasonPrefix + "r\n" + keptA + "\n# an unrelated note\n" + keptB + "\n")
	if len(b.groups) != 1 || len(b.groups[0].entries) != 1 {
		t.Fatalf("groups = %+v, want one group of one entry", b.groups)
	}
	if len(b.plain) != 1 || b.plain[0] != keptB {
		t.Fatalf("plain = %q, want [%q]", b.plain, keptB)
	}
}

func TestRefreshKeepsTheReasonOfAnEntryThatStays(t *testing.T) {
	current := map[string]bool{keptA: true, keptB: true, plainE: true, fresh: true}
	out := parseBaseline(parseBaseline(sampleBaseline()).refresh(".", current).render())

	reasons := map[string]string{}
	for _, g := range out.groups {
		for _, e := range g.entries {
			reasons[e] = g.reason
		}
	}
	if reasons[keptA] != "helpers of the exported pkg API." || reasons[keptB] != reasons[keptA] {
		t.Fatalf("kept entries lost their reason: %v", reasons)
	}
	if reasons[other] != "a generic at its origin." {
		t.Fatalf("another module's reasoned entry was touched: %v", reasons)
	}
	if strings.Join(out.plain, "|") != strings.Join([]string{plainE, fresh}, "|") {
		t.Fatalf("plain = %q, want the new entry and the unreasoned one, sorted", out.plain)
	}
}

func TestWriteBaselineRoundTripsTheReasons(t *testing.T) {
	path := filepath.Join(t.TempDir(), "baseline.txt")
	if err := os.WriteFile(path, []byte(sampleBaseline()), 0o600); err != nil {
		t.Fatal(err)
	}
	findings := []finding{
		{Module: ".", Scope: "internal", Kind: "func", Symbol: "example.com/m/pkg.a"},
		{Module: ".", Scope: "exported", Kind: "func", Symbol: "example.com/m/pkg.Exported"},
	}
	if err := writeBaseline(findings, path, "."); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	want := baselineHeader + plainE + "\n" + reasonsPreamble +
		"#\n" + reasonPrefix + "helpers of the exported pkg API.\n" + keptA + "\n" +
		"#\n" + reasonPrefix + "a generic at its origin.\n" + other + "\n"
	if got != want {
		t.Fatalf("rewritten baseline:\n%s\nwant:\n%s", got, want)
	}
	accepted, err := readBaseline(path)
	if err != nil {
		t.Fatal(err)
	}
	if !accepted[keptA] || !accepted[other] || accepted[leaving] {
		t.Fatalf("accepted set = %v", accepted)
	}
}

func TestWriteBaselineCreatesAMissingFileWithoutReasons(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "baseline.txt")
	findings := []finding{{Module: ".", Scope: "internal", Kind: "func", Symbol: "example.com/m/pkg.a"}}
	if err := writeBaseline(findings, path, "."); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != baselineHeader+keptA+"\n" {
		t.Fatalf("new baseline = %q", data)
	}
}
