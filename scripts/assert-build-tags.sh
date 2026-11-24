#!/bin/sh
# assert-build-tags.sh: the declared build-tag set must select code.
set -eu

[ -n "${GO_BUILD_TAGS:-}" ] || { echo "GO_BUILD_TAGS is empty: nothing to assert"; exit 0; }

# shellcheck disable=SC2016 # a Go template, not a shell expansion
list_files() {
	go list ${1:+-tags "$1"} -f '{{$p := .ImportPath}}{{range .GoFiles}}{{$p}}/{{.}}
{{end}}{{range .CgoFiles}}{{$p}}/{{.}}
{{end}}{{range .TestGoFiles}}{{$p}}/{{.}}
{{end}}{{range .XTestGoFiles}}{{$p}}/{{.}}
{{end}}' ./... 2>/dev/null | sort -u
}

bare=$(list_files "")
full=$(list_files "$GO_BUILD_TAGS")
nb=$(printf '%s\n' "$bare" | grep -c . || true)
nf=$(printf '%s\n' "$full" | grep -c . || true)
echo "files selected: untagged=$nb tagged=$nf (tags: $GO_BUILD_TAGS)"
rc=0
if [ "$full" = "$bare" ]; then
	echo "::error::-tags '$GO_BUILD_TAGS' changed nothing: the same files are selected with and without it. Either it names tags no file declares, or GO_BUILD_TAGS should be empty."
	rc=1
fi
for t in $GO_BUILD_TAGS; do
	# shellcheck disable=SC2086 # word-split on purpose: one tag per line
	rest=$(printf '%s\n' $GO_BUILD_TAGS | grep -vx -- "$t" | tr '\n' ' ')
	if [ "$(list_files "$rest")" = "$full" ]; then
		echo "::error::build tag '$t' selects nothing: removing it from GO_BUILD_TAGS changes no file. Misspelled, or it should be removed."
		rc=1
	fi
done
[ "$rc" -ne 0 ] || echo "OK: every declared tag selects code"
exit "$rc"
