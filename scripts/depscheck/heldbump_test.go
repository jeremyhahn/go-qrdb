package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// graphEditor is a go command over a fake module graph: go get path@version
// raises path to version in go.mod and every requirement of that version
// the graph lists, as minimal version selection would, and records a go.sum
// line for each module it moves.
type graphEditor struct {
	// graph maps path@version to the requirements that version declares.
	graph map[string]map[string]string
	gets  []string
	tidy  int
	// tidied lists the directories tidied, in order.
	tidied []string
}

func (g *graphEditor) Get(dir, pathAtVersion string) (err error) {
	g.gets = append(g.gets, pathAtVersion)
	path, version, _ := strings.Cut(pathAtVersion, "@")
	moves := map[string]string{path: version}
	for p, v := range g.graph[pathAtVersion] {
		moves[p] = v
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := root.Close(); err == nil {
			err = cerr
		}
	}()
	data, err := root.ReadFile("go.mod")
	if err != nil {
		return err
	}
	directives, err := parseGoMod(filepath.Join(dir, "go.mod"), data)
	if err != nil {
		return err
	}
	var sum bytes.Buffer
	for _, d := range directives {
		if v, ok := moves[d.Path]; ok && d.Kind == kindRequire && d.Version != v {
			data = bytes.ReplaceAll(data, []byte(d.Path+" "+d.Version), []byte(d.Path+" "+v))
			sum.WriteString(d.Path + " " + v + " h1:fake=\n")
		}
	}
	if err := root.WriteFile("go.mod", data, 0o600); err != nil {
		return err
	}
	f, err := root.OpenFile("go.sum", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(sum.Bytes()); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (g *graphEditor) Tidy(dir string) error {
	g.tidy++
	g.tidied = append(g.tidied, dir)
	return nil
}

func behindFinding(path, version, latest string) finding {
	return finding{
		Dep:    dependency{File: "go.mod", Kind: kindRequire, Module: path, Path: path, Version: version},
		Behind: true,
		Latest: latest,
	}
}

// requiredVersion reads path's required version out of dir/go.mod.
func requiredVersion(t *testing.T, dir, path string) string {
	t.Helper()
	gomod := filepath.Join(dir, "go.mod")
	data, err := os.ReadFile(gomod) //#nosec G304 -- a test fixture copied into t.TempDir
	require.NoError(t, err)
	directives, err := parseGoMod(gomod, data)
	require.NoError(t, err)
	for _, d := range directives {
		if d.Kind == kindRequire && d.Path == path {
			return d.Version
		}
	}
	return ""
}

// writeRootFile writes a fixture file named name inside dir through an
// os.Root, so the write cannot leave dir.
func writeRootFile(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	defer func() { require.NoError(t, root.Close()) }()
	require.NoError(t, root.WriteFile(name, data, 0o600))
}

func TestBumpNeverMovesAHeldRequirement(t *testing.T) {
	dir := t.TempDir()
	fixture, err := os.ReadFile(filepath.Join("testdata", "heldgraph", "go.mod"))
	require.NoError(t, err)
	writeRootFile(t, dir, "go.mod", fixture)
	sum := []byte("example.com/a v1.0.0 h1:fake=\n")
	writeRootFile(t, dir, "go.sum", sum)

	// a v1.1.0 requires the held b at v1.1.0; c v1.1.0 requires nothing held.
	edit := &graphEditor{graph: map[string]map[string]string{
		"example.com/a@v1.1.0": {"example.com/b": "v1.1.0"},
	}}
	findings := []finding{
		behindFinding("example.com/a", "v1.0.0", "v1.1.0"),
		behindFinding("example.com/c", "v1.0.0", "v1.1.0"),
	}
	var stdout bytes.Buffer
	require.NoError(t, bumpModules(edit, []moduleFindings{{dir: dir, found: findings}}, &stdout))

	assert.Equal(t, "v1.0.0", requiredVersion(t, dir, "example.com/b"), "the held requirement stays where its reason holds it")
	assert.Equal(t, "v1.0.0", requiredVersion(t, dir, "example.com/a"), "the bump that raised the held requirement is reverted")
	assert.Equal(t, "v1.1.0", requiredVersion(t, dir, "example.com/c"), "a bump that leaves every hold alone ships")
	assert.Equal(t, []string{"example.com/a@v1.1.0", "example.com/c@v1.1.0"}, edit.gets)
	assert.Equal(t, 1, edit.tidy)
	gotSum, err := os.ReadFile(filepath.Join(dir, "go.sum")) //#nosec G304 -- t.TempDir
	require.NoError(t, err)
	assert.Equal(t, string(sum)+"example.com/c v1.1.0 h1:fake=\n", string(gotSum), "the reverted bump leaves no go.sum line")
	assert.Contains(t, stdout.String(), "skipped example.com/a v1.0.0 -> v1.1.0: it raises held example.com/b v1.0.0 -> v1.1.0")
	assert.NotContains(t, stdout.String(), "bumped example.com/a")
	assert.Contains(t, stdout.String(), "bumped example.com/c v1.0.0 -> v1.1.0")
}

func TestBumpSkipsAllWhenEveryBumpMovesAHold(t *testing.T) {
	dir := t.TempDir()
	fixture, err := os.ReadFile(filepath.Join("testdata", "heldgraph", "go.mod"))
	require.NoError(t, err)
	writeRootFile(t, dir, "go.mod", fixture)

	edit := &graphEditor{graph: map[string]map[string]string{
		"example.com/a@v1.1.0": {"example.com/b": "v1.1.0"},
	}}
	var stdout bytes.Buffer
	require.NoError(t, bumpModules(edit, []moduleFindings{{dir: dir, found: []finding{behindFinding("example.com/a", "v1.0.0", "v1.1.0")}}}, &stdout))

	got, err := os.ReadFile(filepath.Join(dir, "go.mod")) //#nosec G304 -- t.TempDir
	require.NoError(t, err)
	assert.Equal(t, string(fixture), string(got))
	_, err = os.Stat(filepath.Join(dir, "go.sum"))
	assert.True(t, os.IsNotExist(err), "a go.sum the reverted bump created is removed")
	assert.Equal(t, 0, edit.tidy, "nothing moved, nothing to tidy")
}

func TestBumpTidiesALocallyReplacedModuleWithNoBumpOfItsOwn(t *testing.T) {
	root := t.TempDir()
	fixture, err := os.ReadFile(filepath.Join("testdata", "heldgraph", "go.mod"))
	require.NoError(t, err)
	writeRootFile(t, root, "go.mod", fixture)
	// sdk replaces the root with a local directory and requires nothing the
	// bump moves directly: only the root's upgrade changes its graph.
	sdk := filepath.Join(root, "sdk")
	require.NoError(t, os.Mkdir(sdk, 0o750))
	sdkMod := "module example.com/root/sdk\n\ngo 1.22\n\nrequire example.com/root v0.0.0\n\nreplace example.com/root => ../\n"
	writeRootFile(t, sdk, "go.mod", []byte(sdkMod))

	edit := &graphEditor{graph: map[string]map[string]string{}}
	var stdout bytes.Buffer
	// The dependent is listed first: it is still tidied after the root.
	mods := []moduleFindings{
		{dir: sdk},
		{dir: root, found: []finding{behindFinding("example.com/c", "v1.0.0", "v1.1.0")}},
	}
	require.NoError(t, bumpModules(edit, mods, &stdout))

	assert.Equal(t, []string{"example.com/c@v1.1.0"}, edit.gets)
	assert.Equal(t, []string{root, sdk}, edit.tidied, "the locally replaced module is tidied after the module it replaces")
	assert.Contains(t, stdout.String(), "bumped example.com/c v1.0.0 -> v1.1.0")
}
