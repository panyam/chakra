#!/bin/sh
# Run `go test -race` on package paths in whatever module encloses each one.
#
# The repo is several Go modules, so `go test ./ext/files/...` from the root
# fails with "main module does not contain package". This finds the nearest
# go.mod above each path and runs the test from there, which lets one command
# take any package path in the tree. The dogfood exec allowlist uses it as
# run_test (examples/dogfood/workspace.json).
#
# Usage: scripts/test-pkg.sh ./host/... ./ext/files
set -eu
[ $# -gt 0 ] || { echo "usage: $0 <package path>..." >&2; exit 2; }
root=$(pwd)
status=0
for arg in "$@"; do
	pkg=${arg%/...}
	dir=$(cd "$root/$pkg" 2>/dev/null && pwd) || { echo "no such directory: $arg" >&2; status=1; continue; }
	mod=$dir
	while [ ! -f "$mod/go.mod" ]; do
		[ "$mod" != "$root" ] && [ "$mod" != "/" ] || { echo "no go.mod above $arg" >&2; status=1; continue 2; }
		mod=$(dirname "$mod")
	done
	rel=.${dir#"$mod"}
	case $arg in */...) rel=$rel/... ;; esac
	echo "==> ($mod) go test -race $rel"
	(cd "$mod" && go test -race "$rel") || status=1
done
exit $status
