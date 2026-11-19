package main

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
)

// baseReader reads a module's go.mod as it was at the merge base. found is
// false when the commit has no go.mod in that directory: the change adds
// the module, so every requirement in it is added too.
type baseReader interface {
	GoModAt(dir, commit string) (data []byte, found bool, err error)
}

// gitBase is the baseReader that runs git. safe.directory=* because CI runs
// it in a container whose user does not own the checkout.
type gitBase struct {
	path string
}

func (g gitBase) git(dir string, args ...string) ([]byte, error) {
	path := g.path
	if path == "" {
		path = "git"
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(context.Background(), path, append([]string{"-c", "safe.directory=*", "-C", dir}, args...)...) //#nosec G204 -- the commit comes from CI's merge base, the dir from the Makefile
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &BaseError{Dir: dir, Commit: strings.Join(args, " "), Stderr: stderr.String(), Err: err}
	}
	return stdout.Bytes(), nil
}

func (g gitBase) GoModAt(dir, commit string) ([]byte, bool, error) {
	if _, err := g.git(dir, "rev-parse", "--verify", "--quiet", commit+"^{commit}"); err != nil {
		var be *BaseError
		if errors.As(err, &be) {
			be.Commit = commit
		}
		return nil, false, err
	}
	// ls-tree paths are relative to dir, so this names dir's own go.mod.
	listed, err := g.git(dir, "ls-tree", "--name-only", commit, "--", "go.mod")
	if err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(string(listed)) == "" {
		return nil, false, nil
	}
	data, err := g.git(dir, "show", commit+":./go.mod")
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// baseVersions maps each dependency's module to the path@version it had at
// the merge base.
func baseVersions(file string, data []byte) (map[string]string, error) {
	directives, err := parseGoMod(file, data)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for _, d := range dependencies(file, directives) {
		out[depIdentity(d)] = baseKey(d)
	}
	for path, version := range requireVersions(directives) {
		out[requiresKey+path] = version
	}
	return out, nil
}

// requiresKey prefixes a baseVersions entry holding the version go.mod
// requires of a module, whatever replaces it: it decides whether a
// version-specific replace of that module is active.
const requiresKey = "requires "

// requireVersions is the version each require line of directives names.
func requireVersions(directives []directive) map[string]string {
	out := make(map[string]string)
	for _, d := range directives {
		if d.Kind == kindRequire {
			out[d.Path] = d.Version
		}
	}
	return out
}

// touched reports whether the change adds dep or changes its version or
// path. A nil base (the module is new) touches everything.
func touched(dep dependency, base, requires map[string]string) bool {
	if base == nil {
		return true
	}
	was, ok := base[depIdentity(dep)]
	if !ok || was != baseKey(dep) {
		return true
	}
	// A version-specific replace applies only while its module is at that
	// version: a change of the requirement can activate it unchanged.
	return dep.Kind == kindReplace && dep.Replaces != "" && base[requiresKey+dep.Module] != requires[dep.Module]
}

// baseKey is what the base comparison keys a line on: its target, plus a
// replace's left-hand version, since changing which version a replace
// applies to can make an outdated replacement active.
// depIdentity is the key of a dependency in the base map. A go.mod may hold
// several version-specific replaces of one module (replace A v1.0.0 => ...,
// replace A v1.1.0 => ...), so a replace's identity includes its left-hand
// version: each keeps its own base entry instead of overwriting the others.
func depIdentity(dep dependency) string {
	id := dep.Kind + " " + dep.Module
	if dep.Replaces != "" {
		id += "@" + dep.Replaces
	}
	return id
}

func baseKey(dep dependency) string {
	key := dep.Path + "@" + dep.Version
	if dep.Replaces != "" {
		key += " replaces " + dep.Replaces
	}
	return key
}
