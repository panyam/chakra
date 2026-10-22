#!/bin/sh
# Run `go test -race` on package paths in whatever module encloses each one.
#
# The repo is several Go modules, so `go test ./ext/files/...` from the root
# fails with "main module does not contain package". This finds the nearest
# go.mod above each path and runs the test from there, which lets one command
# take any package path in the tree. The dogfood exec allowlist uses it as
# run_test and run_test_one (examples/dogfood/workspace.json).
#
# A path may carry a test pattern after a colon, ./host:TestFoo, which becomes
# -run. It is one argument because the exec allowlist checks every argument
# against one pattern, so a path and a test name cannot be checked separately.
# A pattern that matches no test fails, since go test exits 0 on it and that
# reads as a pass.
#
# Usage: scripts/test-pkg.sh ./host/... ./ext/files ./surfaces/chat:TestBanner
set -eu
[ $# -gt 0 ] || { echo "usage: $0 <package path>[:<test pattern>]..." >&2; exit 2; }
root=$(pwd)
status=0
for arg in "$@"; do
	run=
	case $arg in *:*) run=${arg#*:} arg=${arg%%:*} ;; esac
	case /$arg/ in */../*) echo "path leaves the workspace: $arg" >&2; status=1; continue ;; esac
	pkg=${arg%/...}
	dir=$(cd "$root/$pkg" 2>/dev/null && pwd) || { echo "no such directory: $arg" >&2; status=1; continue; }
	mod=$dir
	while [ ! -f "$mod/go.mod" ]; do
		[ "$mod" != "$root" ] && [ "$mod" != "/" ] || { echo "no go.mod above $arg" >&2; status=1; continue 2; }
		mod=$(dirname "$mod")
	done
	rel=.${dir#"$mod"}
	case $arg in */...) rel=$rel/... ;; esac
	if [ -z "$run" ]; then
		echo "==> ($mod) go test -race $rel"
		(cd "$mod" && go test -race "$rel") || status=1
		continue
	fi
	echo "==> ($mod) go test -race -run '$run' -v $rel"
	out=$(cd "$mod" && go test -race -run "$run" -v "$rel" 2>&1) || status=1
	printf '%s\n' "$out"
	case $out in
	*"=== RUN"*) ;;
	*) echo "no test matched -run '$run' in $rel" >&2; status=1 ;;
	esac
done
exit $status
