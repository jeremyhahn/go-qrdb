package main

import (
	"bytes"
	"testing"
)

// TestBumpNeverDowngradesAModuleAnEarlierBumpRaised covers one upgrade
// raising another candidate past its planned target: getting the later
// module at its older target would downgrade it, and with it the module
// whose upgrade raised it.
func TestBumpNeverDowngradesAModuleAnEarlierBumpRaised(t *testing.T) {
	dir := t.TempDir()
	writeRootFile(t, dir, "go.mod", []byte("module example.com/raise\n\ngo 1.25\n\nrequire (\n\texample.com/a v1.0.0\n\texample.com/b v1.0.0\n)\n"))
	writeRootFile(t, dir, "go.sum", nil)

	// a v1.1.0 requires b v1.2.0-rc.1, past b's planned v1.1.0; getting
	// b@v1.1.0 would take a back to v1.0.0, as go get does when a
	// downgrade drops below a requirement.
	edit := &graphEditor{graph: map[string]map[string]string{
		"example.com/a@v1.1.0": {"example.com/b": "v1.2.0-rc.1"},
		"example.com/b@v1.1.0": {"example.com/a": "v1.0.0"},
	}}
	findings := []finding{
		behindFinding("example.com/a", "v1.0.0", "v1.1.0"),
		behindFinding("example.com/b", "v1.0.0", "v1.1.0"),
	}
	var stdout bytes.Buffer
	require.NoError(t, bumpModules(edit, []moduleFindings{{dir: dir, found: findings}}, &stdout))

	assert.Equal(t, "v1.1.0", requiredVersion(t, dir, "example.com/a"), "the earlier upgrade is kept")
	assert.Equal(t, "v1.2.0-rc.1", requiredVersion(t, dir, "example.com/b"), "the raised module is not taken back to its planned target")
	assert.Equal(t, []string{"example.com/a@v1.1.0"}, edit.gets, "no get downgrades a module already at or past its target")
	assert.Contains(t, stdout.String(), "bumped example.com/a v1.0.0 -> v1.1.0")
	assert.Contains(t, stdout.String(), "bumped example.com/b v1.0.0 -> v1.2.0-rc.1", "the report names the version actually selected")
	assert.NotContains(t, stdout.String(), "example.com/b v1.0.0 -> v1.1.0")
}
