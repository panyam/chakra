#!/usr/bin/env python3
"""Fail when a github.com/panyam/* require names a version that was never
tagged.

Why this exists
---------------
Every module replaces mcpkit with ../mcpkit/main, so a tidy records whatever
versions mcpkit main declares, and mcpkit bumps its sub-module requires a little
before it pushes the tag. Locally the replace hides that. From outside, the
require cannot resolve, and the repo's rule is to depend on tagged releases
only. The tidy workflow runs this before opening a PR, so a tidy caught
mid-release waits for the next run instead of proposing an untagged pin (#73).

Each module@version is resolved through the module proxy from a scratch
directory, outside any module, so no replace applies. Needs the network, so it
is not part of `make testall`.
"""
import os
import subprocess
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from check_dep_consistency import INTERNAL_PREFIX, REPO_ROOT, modules, requires  # noqa: E402

PREFIX = "github.com/panyam/"


def main():
    pins = set()
    for gomod in modules():
        for mod, ver in requires(gomod):
            if not mod.startswith(PREFIX):
                continue
            if mod == INTERNAL_PREFIX or mod.startswith(INTERNAL_PREFIX + "/"):
                continue
            pins.add((mod, ver))

    env = dict(os.environ, GOWORK="off", GOFLAGS="")
    missing = []
    with tempfile.TemporaryDirectory() as scratch:
        for mod, ver in sorted(pins):
            r = subprocess.run(["go", "list", "-m", f"{mod}@{ver}"], cwd=scratch, env=env,
                               capture_output=True, text=True)
            if r.returncode != 0:
                missing.append((mod, ver, r.stderr.strip().splitlines()[-1]))

    if not missing:
        print(f"check_tagged_requires: {len(pins)} panyam pins, all tagged.")
        return 0
    print("check_tagged_requires: these pins do not resolve from outside the repo.\n", file=sys.stderr)
    for mod, ver, why in missing:
        print(f"  {mod}@{ver}\n    {why}", file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())
