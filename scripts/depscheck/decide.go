package main

import (
	"errors"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

// forkLatest stands in for the latest version of a replace onto a different
// module path, which no version comparison can judge.
const forkLatest = "n/a (replaced by a different module)"

// finding is the verdict on one dependency.
type finding struct {
	Dep    dependency
	Latest string
	Behind bool
	// Unused is set for a replace that applies to no module in the build list.
	Unused bool
	// Lookup is set when the latest version could not be looked up.
	Lookup *LookupError
	// Fresh is set when Latest was released less than freshWindow ago:
	// an undocumented lag behind it is reported, not failed.
	Fresh bool
	// Released is when Latest was released; zero when unknown.
	Released time.Time
	// Untouched is set, under -base, when the change leaves this line's
	// path and version as they were at the merge base: a lag behind latest
	// is then reported, not failed (ruling 640).
	Untouched bool
}

// freshWindow is how long a new release may go unadopted before a lag
// behind it, with no go.mod reason comment, fails the check.
const freshWindow = 7 * 24 * time.Hour

// notKnownDependency is the go command's text for a module that is not in
// the build list ("module M: not a known dependency").
const notKnownDependency = "not a known dependency"

// assess judges one dependency against its `go list` entry. listed is false
// when the listing has no entry for the module at all.
//
// The latest version is, for a first-party module (path under firstParty),
// the highest published tag including -alpha prereleases, the version
// `make deps-latest` moves to (dependency-resolution.md §3). For a
// third-party require it is the go command's own Update answer, which
// prefers releases and skips retractions. A replace has no Update of its
// own, so for it the latest is the highest release in Versions.
//
// now is the clock the release age is measured against. Only the Update
// answer carries a release time here; for any other latest version
// (needsReleaseTime) checkModule looks up that tag's own time.
func assess(dep dependency, mod listedModule, listed bool, firstParty string, now time.Time) finding {
	f := finding{Dep: dep}
	switch {
	case !listed:
		f.Lookup = &LookupError{Module: dep.Module, Reason: "go list returned no entry for it"}
		return f
	case dep.Kind == kindReplace && mod.Version == "" && (mod.Error == nil || strings.Contains(mod.Error.Err, notKnownDependency)):
		// The replaced module is not in the build list: the go command
		// lists it with no version and, depending on the query, no error
		// or "not a known dependency". The replace applies to nothing.
		f.Unused = true
		return f
	case mod.Error != nil:
		f.Lookup = &LookupError{Module: dep.Module, Reason: mod.Error.Err}
		return f
	case mod.Version == "":
		f.Lookup = &LookupError{Module: dep.Module, Reason: notKnownDependency}
		return f
	case dep.Path != dep.Module:
		// A fork: holding it is always a decision that needs its reason.
		f.Latest, f.Behind = forkLatest, true
		return f
	}
	f.Latest = latestVersion(dep, mod, strings.HasPrefix(dep.Path, firstParty))
	f.Behind = isBehind(dep.Version, f.Latest)
	if f.Behind && mod.Update != nil && mod.Update.Version == f.Latest {
		f.Released = mod.Update.Time
		f.Fresh = isFresh(f.Released, now)
	}
	return f
}

// isFresh reports whether a release at released is younger than
// freshWindow at now. An unknown (zero) release time is never fresh.
func isFresh(released, now time.Time) bool {
	if released.IsZero() {
		return false
	}
	return now.Sub(released) < freshWindow
}

// needsReleaseTime reports whether a behind finding's Latest is a tag the
// listing's Update does not describe, such as a first-party -alpha newer
// than the latest release or a replace's highest tag, so its release age is
// unknown until that tag's own publication time is looked up. Without it a
// fresh tag would be judged old: failed, or bumped to, inside the window.
func needsReleaseTime(f finding, mod listedModule) bool {
	return f.Behind && f.Lookup == nil && f.Latest != forkLatest &&
		(mod.Update == nil || mod.Update.Version != f.Latest)
}

// lookupError reports a failed release-time lookup as module's lookup
// failure.
func lookupError(module string, err error) *LookupError {
	var lookup *LookupError
	if errors.As(err, &lookup) {
		return lookup
	}
	return &LookupError{Module: module, Reason: err.Error()}
}

func latestVersion(dep dependency, mod listedModule, firstParty bool) string {
	if dep.Kind == kindRequire && !firstParty {
		if mod.Update != nil {
			return mod.Update.Version
		}
		return dep.Version
	}
	candidates := mod.Versions
	if !firstParty {
		if releases := releasesOnly(mod.Versions); len(releases) > 0 {
			candidates = releases
		}
	}
	if len(candidates) > 0 {
		return candidates[len(candidates)-1]
	}
	if mod.Update != nil {
		return mod.Update.Version
	}
	return dep.Version
}

// isBehind orders current and latest semantically, so a pseudo-version
// (absent from the published tag list) built on a commit past the latest tag
// is not behind it. A latest that is not a semantic version is the fork
// marker, which is always behind.
func isBehind(current, latest string) bool {
	if latest == current {
		return false
	}
	if !semver.IsValid(latest) {
		return true
	}
	return semver.Compare(current, latest) < 0
}

func releasesOnly(versions []string) []string {
	var releases []string
	for _, v := range versions {
		if !isPrerelease(v) {
			releases = append(releases, v)
		}
	}
	return releases
}

// isPrerelease reports a semantic-version prerelease, including a
// pseudo-version: a "-" before any "+build" suffix, since the
// vMAJOR.MINOR.PATCH core never contains one.
func isPrerelease(version string) bool {
	core, _, _ := strings.Cut(version, "+")
	return strings.Contains(core, "-")
}
