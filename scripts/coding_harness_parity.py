"""Print codex exec and chakra's coding harness side by side.

Usage: coding_harness_parity.py <fixture> <out>, where <out> is what
scripts/coding-harness-parity.sh wrote: codex/{events.jsonl,last.json,wall,repo}
and chakra/{metrics.json,events.jsonl}.
"""

import json
import os
import subprocess
import sys

REQUIRED = ["status", "summary", "files_changed", "tests_passed"]

# stats/AGENTS.md asks for this line at the end of the final answer. Only an
# agent that reads that subdirectory's file (on its own, or because the harness
# loads it once the agent works there) knows to write it.
STATS_RULE = "stats: checked"


def jsonl(path):
    try:
        with open(path) as f:
            return [json.loads(line) for line in f if line.strip()]
    except FileNotFoundError:
        return []


def tests_pass(repo):
    if not os.path.isdir(repo):
        return None
    return subprocess.run(["go", "test", "./..."], cwd=repo, capture_output=True).returncode == 0


def test_files_changed(fixture, repo):
    changed = []
    for dirpath, _, names in os.walk(fixture):
        for n in names:
            if not n.endswith("_test.go"):
                continue
            rel = os.path.relpath(os.path.join(dirpath, n), fixture)
            try:
                same = open(os.path.join(fixture, rel)).read() == open(os.path.join(repo, rel)).read()
            except FileNotFoundError:
                same = False
            if not same:
                changed.append(rel)
    return changed


def codex_side(fixture, out):
    events = jsonl(os.path.join(out, "codex", "events.jsonl"))
    usage, tools, commands = {}, 0, []
    for e in events:
        if e.get("type") == "turn.completed":
            usage = e.get("usage", {})
        item = e.get("item", {})
        if e.get("type") == "item.completed" and item.get("type") in ("command_execution", "file_change", "mcp_tool_call", "web_search"):
            tools += 1
            if item.get("type") == "command_execution":
                commands.append(item.get("command", ""))
    try:
        result = json.load(open(os.path.join(out, "codex", "last.json")))
        schema_ok = all(k in result for k in REQUIRED)
    except (FileNotFoundError, json.JSONDecodeError):
        schema_ok = False
    repo = os.path.join(out, "codex", "repo")
    return {
        "steps": "n/a",
        "tool_calls": tools,
        "input_tokens": usage.get("input_tokens", 0),
        "cached_input": usage.get("cached_input_tokens", 0),
        "output_tokens": usage.get("output_tokens", 0),
        "wall_seconds": int(open(os.path.join(out, "codex", "wall")).read().strip() or 0),
        "tests_passed": tests_pass(repo),
        "schema_valid": schema_ok,
        "test_files_untouched": not test_files_changed(fixture, repo),
        "stats_rule_followed": STATS_RULE in last_text(out),
        "commands": commands,
    }


def chakra_side(out):
    try:
        m = json.load(open(os.path.join(out, "chakra", "metrics.json")))
    except FileNotFoundError:
        return None
    tools = []
    text = ""
    for fr in jsonl(os.path.join(out, "chakra", "events.jsonl")):
        ev = (fr.get("payload") or {}).get("RunnerEvent") or {}
        if ev.get("kind") == "tool-begin":
            tools.append((ev.get("toolCall") or {}).get("name", ""))
        elif ev.get("kind") == "text-delta":
            text += ev.get("text", "")
    return {
        "steps": m.get("steps"),
        "tool_calls": m.get("tool_calls"),
        "input_tokens": m.get("input_tokens"),
        "cached_input": m.get("cached_input_tokens", "n/a"),
        "output_tokens": m.get("output_tokens"),
        "wall_seconds": m.get("wall_seconds"),
        "tests_passed": m.get("tests_passed"),
        "schema_valid": m.get("schema_valid", False),
        "test_files_untouched": m.get("test_files_untouched"),
        "stats_rule_followed": STATS_RULE in text,
        "commands": tools,
        "by_model": m.get("by_model") or {},
    }


def last_text(out):
    try:
        return open(os.path.join(out, "codex", "last.json")).read()
    except FileNotFoundError:
        return ""


def main():
    fixture, out = sys.argv[1], sys.argv[2]
    cx, ck = codex_side(fixture, out), chakra_side(out)
    rows = ["steps", "tool_calls", "input_tokens", "cached_input", "output_tokens", "wall_seconds", "tests_passed", "schema_valid", "test_files_untouched", "stats_rule_followed"]
    print()
    print(f"{'':22}{'codex':>12}{'chakra':>12}")
    for r in rows:
        print(f"{r:22}{str(cx.get(r)):>12}{str(ck.get(r) if ck else 'no run'):>12}")
    if ck and ck["by_model"]:
        print("\nchakra steps were routed (#185); tokens by model, in / cached / out:")
        for name, u in sorted(ck["by_model"].items()):
            print(f"  {name or 'default':20}{u.get('inputTokens', 0):>10}{u.get('cachedInputTokens', 0):>10}{u.get('outputTokens', 0):>10}")
    print()
    print(f"chakra loaded instruction files with the {os.environ.get('PARITY_INSTRUCTIONS') or 'workspace.json'} preset.")
    print("\ncodex commands:")
    for c in cx["commands"]:
        print("  " + c)
    if ck:
        print("chakra tool calls:\n  " + ", ".join(ck["commands"]))


if __name__ == "__main__":
    main()
