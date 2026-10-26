#!/bin/sh
# Run `go test -race` on package paths in whatever module encloses each one.
#
# The repo is several Go modules, so `go test ./ext/files/...` from the root
# fails with "main module does not contain package". This finds the nearest
# go.mod above each path and runs the test from there, which lets one command
# take any package path in the tree. The dogfood exec allowlist uses it as
# run_test and run_test_one (examples/dogfood/workspace.json).
#
# A /... path above several modules runs in each of them, since one path can
# cover a directory that is not a module itself: ./ext/... is nothing to the
# root module, whose ext/ has no Go files, and everything to ext/checkpoint,
# ext/files, ext/exec and ext/lsp. The target sits at the nearest go.mod above
# it for the single-module case, and at each go.mod below it for this one.
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
failed() { status=1; }

# ismod reports whether a directory holds a Go module, the root itself included.
ismod() { [ "$1" = "$root" ] || [ -f "$1/go.mod" ]; }

# modsbelow prints every module below a directory, as a path relative to it.
# The root is not one of them: a path is either the root module's or some other
# module's, and ./... under the root is run_test_all's job.
modsbelow() {
	(cd "$1" && find . -name go.mod -not -path '*/node_modules/*' | sed 's|^\./||; s|/go\.mod$||; s|^go\.mod$|.|' | sort)
}

# testmod runs go test for one path inside one module. $1 is the module
# directory, $2 the path relative to it ("./..." included), $3 the -run pattern
# (empty for all tests) and $4 the argument as the caller wrote it, for
# messages, since a submodule run's relative path does not name it.
testmod() {
	mod=$1 rel=$2 run=$3 arg=$4
	if [ -z "$run" ]; then
		echo "==> ($mod) go test -race $rel"
		(cd "$mod" && go test -race "$rel") || failed
		return
	fi
	echo "==> ($mod) go test -race -run '$run' -v $rel"
	out=$(cd "$mod" && go test -race -run "$run" -v "$rel" 2>&1) || failed
	printf '%s\n' "$out"
	case $out in
	*"=== RUN"*) ;;
	*) echo "no test matched -run '$run' in $arg" >&2; failed ;;
	esac
}

for arg in "$@"; do
	run=
	case $arg in *:*) run=${arg#*:} arg=${arg%%:*} ;; esac
	case /$arg/ in */../*) echo "path leaves the workspace: $arg" >&2; status=1; continue ;; esac
	pkg=${arg%/...}
	dir=$(cd "$root/$pkg" 2>/dev/null && pwd) || { echo "no such directory: $arg" >&2; status=1; continue; }
	mod=$dir
	while ! ismod "$mod"; do
		[ "$mod" != "/" ] || { echo "no go.mod above $arg" >&2; status=1; continue 2; }
		mod=$(dirname "$mod")
	done
	case $arg in
	*/...)
		# A directory below the enclosing module may hold modules of its own,
		# which the enclosing module's ./... does not reach.
		mods=$(modsbelow "$dir")
		if [ -n "$mods" ]; then
			# Each submodule is its own main module, so its packages are all
			# of it: the path within it is ./... from its root.
			for sub in $mods; do
				if [ "$sub" = . ]; then submod=$dir; else submod=$dir/$sub; fi
				testmod "$submod" ./... "$run" "$arg"
			done
			continue
		fi
		;;
	esac
	rel=.${dir#"$mod"}
	case $arg in */...) rel=$rel/... ;; esac
	testmod "$mod" "$rel" "$run" "$arg"
done
exit $status
