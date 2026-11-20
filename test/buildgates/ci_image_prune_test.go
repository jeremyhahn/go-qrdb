package buildgates

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// pruneStubDocker is a docker stand-in for `scripts/ci-image.sh prune`. The
// images it holds are lines "<id> <repository>:<tag>" in $STUB_DIR/images;
// every image was created and tagged long ago, so every one is past any
// bound. Like the daemon, it refuses a removal by ID while more than one tag
// names the image unless --force is given, and rmi drops one tag by name.
const pruneStubDocker = `#!/usr/bin/env bash
state=$STUB_DIR/images
case "$1 $2" in
"ps -q") exit 0 ;;
"image ls")
	while read -r id ref; do printf '%s %s\n' "$id" "$ref"; done <"$state"
	exit 0 ;;
"image inspect")
	shift 2
	fmt=""
	if [ "$1" = -f ]; then fmt=$2; shift 2; fi
	while read -r id ref; do
		if [ "$id" = "$1" ] || [ "$ref" = "$1" ]; then
			case $fmt in
			'{{.Id}}') echo "$id" ;;
			'') ;;
			*) echo "2020-01-01T00:00:00Z 0001-01-01T00:00:00Z" ;;
			esac
			exit 0
		fi
	done <"$state"
	exit 1 ;;
"image rm")
	shift 2
	force=""
	if [ "$1" = --force ]; then force=1; shift; fi
	n=0
	while read -r id ref; do [ "$id" = "$1" ] && n=$((n + 1)); done <"$state"
	[ "$n" -gt 0 ] || exit 1
	[ "$n" -eq 1 ] || [ -n "$force" ] || exit 1
	keep=""
	while read -r id ref; do [ "$id" = "$1" ] || keep+="$id $ref"$'\n'; done <"$state"
	printf '%s' "$keep" >"$state"
	exit 0 ;;
esac
if [ "$1" = rmi ]; then
	keep="" hit=""
	while read -r id ref; do
		if [ "$ref" = "$2" ]; then hit=1; else keep+="$id $ref"$'\n'; fi
	done <"$state"
	printf '%s' "$keep" >"$state"
	[ -n "$hit" ]
	exit
fi
exit 1
`

// TestCIImagePrune_RemovesExpiredLANRegistryImages holds a pool job's images:
// each was pulled from the LAN CI registry and then given the local
// content-hash tag, so it carries both. Once expired, prune removes both
// images; an expired image that carries a tag of no CI image stays.
func TestCIImagePrune_RemovesExpiredLANRegistryImages(t *testing.T) {
	const registry = "registry.example:5000"
	toolchainID := "sha256:" + strings.Repeat("a", 64)
	itID := "sha256:" + strings.Repeat("b", 64)
	foreignID := "sha256:" + strings.Repeat("c", 64)
	images := toolchainID + " " + registry + "/go-qrdb/toolchain-image:0123456789abcdef-1\n" +
		toolchainID + " qrdb-toolchain:0123456789abcdef\n" +
		itID + " " + registry + "/go-qrdb/it-image:fedcba9876543210-1\n" +
		itID + " qrdb-ci-it:fedcba9876543210\n" +
		foreignID + " debian:12\n"

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(pruneStubDocker), 0o700); err != nil { // #nosec G306 -- the stub must be executable
		t.Fatal(err)
	}
	state := filepath.Join(dir, "images")
	if err := os.WriteFile(state, []byte(images), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(repoRoot(t), "scripts", "ci-image.sh")
	cmd := exec.Command("bash", script, "prune", "24") // #nosec G204 -- the repository's own script
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"STUB_DIR="+dir, "CI_REGISTRY="+registry)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("prune failed: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(raw)), foreignID+" debian:12"; got != want {
		t.Fatalf("after prune the daemon holds\n%s\nwant only the foreign image\n%s\nprune said:\n%s", got, want, out)
	}
}

// TestCIImagePrune_RegistryCacheTags pins that a registry-cache tag left by
// scripts/image-registry.sh ci_registry_push (<registry>/qrdb-toolchain:<hash>,
// <registry>/qrdb-ci-it:<hash>) counts as a CI tag (#1596): an expired image
// carrying it beside its local CI tag is pruned, and one carrying it beside a
// foreign tag is kept.
func TestCIImagePrune_RegistryCacheTags(t *testing.T) {
	const registry = "registry.example:5000"
	cachedID := "sha256:" + strings.Repeat("d", 64)
	mixedID := "sha256:" + strings.Repeat("e", 64)
	kept := mixedID + " " + registry + "/qrdb-ci-it:fedcba9876543210\n" +
		mixedID + " debian:12"
	images := cachedID + " qrdb-toolchain:0123456789abcdef\n" +
		cachedID + " " + registry + "/qrdb-toolchain:0123456789abcdef\n" +
		kept + "\n"

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(pruneStubDocker), 0o700); err != nil { // #nosec G306 -- the stub must be executable
		t.Fatal(err)
	}
	state := filepath.Join(dir, "images")
	if err := os.WriteFile(state, []byte(images), 0o600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(repoRoot(t), "scripts", "ci-image.sh")
	cmd := exec.Command("bash", script, "prune", "24") // #nosec G204 -- the repository's own script
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"STUB_DIR="+dir, "CI_IMAGE_LOCK="+filepath.Join(dir, "image.lock"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("prune failed: %v\n%s", err, out)
	}
	raw, err := os.ReadFile(state)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(raw)); got != kept {
		t.Fatalf("after prune the daemon holds\n%s\nwant only the image with a foreign tag\n%s\nprune said:\n%s", got, kept, out)
	}
}
