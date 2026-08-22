#!/bin/sh
# Pre-push hook for chakra.
#
# Runs the cheap structural gates plus the module test suites before pushing.
# The gates are first and take about a second between them, so the common
# failure (a stray binary, a dependency split, a cross-extension require) is
# reported before you wait on tests.
#
# Installed by `make setup-hooks`. Edit this file in the repo, then re-run
# `make setup-hooks` to reinstall.

set -e
cd "$(git rev-parse --show-toplevel)"

# Determine the commit range being pushed. Prefer @{upstream}; fall back to
# origin/main; if both fail (detached HEAD, fresh branch with no remote), treat
# it as an unknown range and run everything.
range=""
if git rev-parse --abbrev-ref --symbolic-full-name '@{upstream}' >/dev/null 2>&1; then
    range="@{upstream}..HEAD"
elif git rev-parse --verify origin/main >/dev/null 2>&1; then
    range="origin/main..HEAD"
fi

changed=""
if [ -n "$range" ]; then
    changed="$(git diff --name-only "$range" 2>/dev/null || true)"
fi

# Docs-only pushes skip the test run. The gates still run: a binary can be
# committed alongside a README, and check-no-binaries is the layer that catches
# it regardless of what else is in the push.
needs_tests=1
if [ -n "$changed" ]; then
    all_docs=1
    for f in $changed; do
        case "$f" in
            *.md|docs/*) ;;
            *) all_docs=0; break ;;
        esac
    done
    [ $all_docs -eq 1 ] && needs_tests=0
fi

echo "pre-push: structural gates"
sh scripts/check-no-binaries.sh
sh scripts/check-ext-isolation.sh
python3 scripts/check_dep_consistency.py

if [ $needs_tests -eq 0 ]; then
    echo "pre-push: docs-only change, skipping the test run"
    exit 0
fi

echo "pre-push: module tests"
make test

echo "pre-push: example tests"
make test-examples
