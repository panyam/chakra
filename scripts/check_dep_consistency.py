#!/usr/bin/env python3
"""Fail when a third-party dependency is pinned at more than one version across
this repo's modules.

Why this exists
---------------
Every module here replaces its siblings by relative path, so intra-repo deps
resolve locally while third-party ones resolve through MVS. MVS takes the
*maximum* required version across the graph, so bumping a dependency in one
module silently raises the version its siblings build against. If that bump is
API-breaking, the sibling breaks without its go.mod ever changing, and the
failure surfaces in a module nobody edited.

Running it per-PR catches divergence on the change that introduces it.

Scope
-----
chakra's own modules are skipped: they resolve via `replace`, so their recorded
versions are placeholders rather than claims about what gets built. mcpkit's
modules are in scope, because those are real external pins and a split there is
exactly the failure this catches.

Stdlib only, so CI needs no install step.
"""
import os
import re
import sys
from collections import defaultdict

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
INTERNAL_PREFIX = "github.com/panyam/chakra"
REQUIRE_RE = re.compile(r"^\s+(\S+)\s+(v\S+?)(?:\s+//.*)?$")


def modules():
    for root, dirs, files in os.walk(REPO_ROOT):
        dirs[:] = [d for d in dirs if d not in (".git", "node_modules", "vendor")]
        if "go.mod" in files:
            yield os.path.join(root, "go.mod")


def requires(gomod):
    """Every require line, direct and indirect. Indirect counts: MVS resolves
    the whole graph, so an indirect split raises the built version just the
    same."""
    in_block = False
    with open(gomod) as fh:
        for line in fh:
            line = line.rstrip("\n")
            if line.startswith("require ("):
                in_block = True
                continue
            if in_block and line.strip() == ")":
                in_block = False
                continue
            if line.startswith("replace") or line.startswith("exclude"):
                continue
            if in_block:
                m = REQUIRE_RE.match(line)
                if m:
                    yield m.group(1), m.group(2)
            elif line.startswith("require "):
                parts = line.split()
                if len(parts) >= 3:
                    yield parts[1], parts[2]


def main():
    versions = defaultdict(lambda: defaultdict(list))
    for gomod in sorted(modules()):
        rel = os.path.relpath(os.path.dirname(gomod), REPO_ROOT)
        for mod, ver in requires(gomod):
            if mod == INTERNAL_PREFIX or mod.startswith(INTERNAL_PREFIX + "/"):
                continue
            versions[mod][ver].append(rel)

    split = {m: v for m, v in versions.items() if len(v) > 1}
    if not split:
        print(f"check_dep_consistency: {len(versions)} third-party deps, no version splits.")
        return 0

    print("check_dep_consistency: a dependency is pinned at more than one version.\n", file=sys.stderr)
    for mod in sorted(split):
        print(f"  {mod}", file=sys.stderr)
        for ver in sorted(split[mod]):
            where = ", ".join(sorted(split[mod][ver]))
            print(f"    {ver:<40} {where}", file=sys.stderr)
        print(file=sys.stderr)
    print("MVS builds every module against the maximum, so the lower pins are fiction.", file=sys.stderr)
    print("Fix with: make tidy-all, or bump the lagging module explicitly.", file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(main())
