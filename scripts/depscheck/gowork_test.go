package main

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// goModSum is the go.sum h1 hash of a go.mod file: dirhash.Hash1 over the
// one file named "go.mod".
func goModSum(data []byte) string {
	summary := fmt.Sprintf("%x  %s\n", sha256.Sum256(data), "go.mod")
	sum := sha256.Sum256([]byte(summary))
	return "h1:" + base64.StdEncoding.EncodeToString(sum[:])
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	}
}

// TestListIgnoresGoWork checks a module that sits under a go.work whose
// other module selects a newer version of a shared dependency: the checked
// go.mod's own, outdated requirement is still the one reported.
func TestListIgnoresGoWork(t *testing.T) {
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command on PATH")
	}
	proxy := t.TempDir()
	sharedMod := "module example.com/shared\n\ngo 1.21\n"
	writeFiles(t, proxy, map[string]string{
		"example.com/shared/@v/list":        "v1.0.0\nv1.1.0\n",
		"example.com/shared/@v/v1.0.0.info": `{"Version":"v1.0.0","Time":"2026-01-01T00:00:00Z"}`,
		"example.com/shared/@v/v1.0.0.mod":  sharedMod,
		"example.com/shared/@v/v1.1.0.info": `{"Version":"v1.1.0","Time":"2026-02-01T00:00:00Z"}`,
		"example.com/shared/@v/v1.1.0.mod":  sharedMod,
	})
	sum := goModSum([]byte(sharedMod))
	work := t.TempDir()
	writeFiles(t, work, map[string]string{
		"go.work":        "go 1.21\n\nuse (\n\t./checked\n\t./other\n)\n",
		"checked/go.mod": "module example.com/checked\n\ngo 1.21\n\nrequire example.com/shared v1.0.0\n",
		"checked/go.sum": "example.com/shared v1.0.0/go.mod " + sum + "\n",
		"other/go.mod":   "module example.com/other\n\ngo 1.21\n\nrequire example.com/shared v1.1.0\n",
		"other/go.sum":   "example.com/shared v1.1.0/go.mod " + sum + "\n",
	})
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(proxy))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOFLAGS", "-modcacherw")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOMODCACHE", t.TempDir())
	t.Setenv("GOWORK", "")

	got, err := goCommand{path: goPath}.List(filepath.Join(work, "checked"), []string{"example.com/shared"})
	require.NoError(t, err)
	mod, ok := got["example.com/shared"]
	require.True(t, ok)
	require.Nil(t, mod.Error)
	assert.Equal(t, "v1.0.0", mod.Version, "the checked go.mod's requirement, not the workspace's")
	require.NotNil(t, mod.Update)
	assert.Equal(t, "v1.1.0", mod.Update.Version)
}

func TestAssessUnusedVersionedReplace(t *testing.T) {
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	replace := dependency{Kind: kindReplace, Module: "example.com/gone", Path: "example.com/gone", Version: "v0.4.1"}
	notKnown := &struct{ Err string }{Err: "module example.com/gone: not a known dependency"}

	f := assess(replace, listedModule{Path: "example.com/gone"}, true, "", now)
	assert.True(t, f.Unused, "no version and no error: not in the build list")
	assert.Nil(t, f.Lookup)

	f = assess(replace, listedModule{Path: "example.com/gone", Error: notKnown}, true, "", now)
	assert.True(t, f.Unused, "not a known dependency: not in the build list")
	assert.Nil(t, f.Lookup)

	f = assess(replace, listedModule{Path: "example.com/gone", Error: &struct{ Err string }{Err: "reading list: 404"}}, true, "", now)
	assert.False(t, f.Unused, "any other error is a lookup failure")
	require.NotNil(t, f.Lookup)

	required := dependency{Kind: kindRequire, Module: "example.com/gone", Path: "example.com/gone", Version: "v0.4.1"}
	f = assess(required, listedModule{Path: "example.com/gone", Error: notKnown}, true, "", now)
	assert.False(t, f.Unused, "a require line is never Unused")
	require.NotNil(t, f.Lookup, "a required module the go command does not know is a lookup failure")
}

// TestListUnusedVersionedReplace runs the go command on a go.mod whose
// versioned replace names a module outside the build list: what it lists
// is classified Unused, not a lookup failure.
func TestListUnusedVersionedReplace(t *testing.T) {
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command on PATH")
	}
	proxy := t.TempDir()
	writeFiles(t, proxy, map[string]string{
		"example.com/gone/@v/list":        "v0.4.1\n",
		"example.com/gone/@v/v0.4.1.info": `{"Version":"v0.4.1","Time":"2026-01-01T00:00:00Z"}`,
		"example.com/gone/@v/v0.4.1.mod":  "module example.com/gone\n",
	})
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{
		"go.mod": "module example.com/m\n\ngo 1.21\n\nreplace example.com/gone => example.com/gone v0.4.1\n",
	})
	t.Setenv("GOPROXY", "file://"+filepath.ToSlash(proxy))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOFLAGS", "-modcacherw")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOMODCACHE", t.TempDir())

	got, err := goCommand{path: goPath}.List(dir, []string{"example.com/gone"})
	require.NoError(t, err)
	mod, ok := got["example.com/gone"]
	require.True(t, ok)
	dep := dependency{Kind: kindReplace, Module: "example.com/gone", Path: "example.com/gone", Version: "v0.4.1"}
	f := assess(dep, mod, ok, "", time.Now())
	assert.True(t, f.Unused, mod)
	assert.Nil(t, f.Lookup)
}
