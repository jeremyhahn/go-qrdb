//go:build clusterproof

package container

import (
	"fmt"
	"strings"
	"testing"
)

// statusAuditMarker marks the audit line every admin:status call writes.
// BOOT-01 polls status once a second on every node, so a failed boot's log
// tail is otherwise nothing but these lines.
const statusAuditMarker = "resource=admin:status"

// withoutStatusAudit returns the last keep lines of logs that do not carry
// statusAuditMarker, and how many status audit lines it dropped.
func withoutStatusAudit(logs string, keep int) (string, int) {
	lines := strings.Split(strings.TrimRight(logs, "\n"), "\n")
	kept := make([]string, 0, len(lines))
	dropped := 0
	for _, l := range lines {
		if strings.Contains(l, statusAuditMarker) {
			dropped++
			continue
		}
		kept = append(kept, l)
	}
	if len(kept) > keep {
		kept = kept[len(kept)-keep:]
	}
	return strings.Join(kept, "\n"), dropped
}

func TestWithoutStatusAudit(t *testing.T) {
	var b strings.Builder
	for i := range 300 {
		fmt.Fprintf(&b, "raft line %d\n", i)
		fmt.Fprintf(&b, "audit op=read resource=admin:status ok %d\n", i)
	}
	got, dropped := withoutStatusAudit(b.String(), 200)
	if dropped != 300 {
		t.Fatalf("dropped %d status audit lines, want 300", dropped)
	}
	lines := strings.Split(got, "\n")
	if len(lines) != 200 {
		t.Fatalf("kept %d lines, want 200", len(lines))
	}
	if lines[0] != "raft line 100" || lines[199] != "raft line 299" {
		t.Fatalf("kept %q .. %q, want the last 200 raft lines", lines[0], lines[199])
	}
	if strings.Contains(got, statusAuditMarker) {
		t.Fatal("a status audit line survived the filter")
	}
	other := "audit op=write resource=admin:config ok"
	if got, _ := withoutStatusAudit(other+"\n", 200); got != other {
		t.Fatalf("other audit line = %q, want it kept", got)
	}
}
