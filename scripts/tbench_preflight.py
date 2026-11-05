"""Checks the model's API answers before a Terminal-Bench run starts (#266).

Usage: tbench_preflight.py <provider/model>

It makes one tiny request with the key Harbor will pass to the agents. An
exhausted account, a bad key or an unknown model stops the run here, with one
clear line, rather than as N trials that each fail in seconds and read as 0/N.
A rate limit or an overloaded API only warns, since a run retries those.
Exit codes: 0 go ahead, 1 stop.
"""

import json
import os
import sys
import urllib.error
import urllib.request


def post(url, headers, body):
    req = urllib.request.Request(url, data=json.dumps(body).encode(), headers=headers, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            return resp.status, json.loads(resp.read() or b"{}")
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read() or b"{}")
        except ValueError:
            return e.code, {}


def openai_request(model, env):
    key = env.get("OPENAI_API_KEY")
    if not key:
        return None, "OPENAI_API_KEY is not set"
    base = (env.get("OPENAI_BASE_URL") or "https://api.openai.com/v1").rstrip("/")
    return (f"{base}/responses",
            {"Authorization": f"Bearer {key}", "Content-Type": "application/json"},
            {"model": model, "input": "ping", "max_output_tokens": 16}), None


def anthropic_request(model, env):
    key = env.get("ANTHROPIC_API_KEY")
    if not key:
        return None, "ANTHROPIC_API_KEY is not set"
    base = (env.get("ANTHROPIC_BASE_URL") or "https://api.anthropic.com").rstrip("/")
    return (f"{base}/v1/messages",
            {"x-api-key": key, "anthropic-version": "2023-06-01", "Content-Type": "application/json"},
            {"model": model, "max_tokens": 1, "messages": [{"role": "user", "content": "ping"}]}), None


REQUESTS = {"openai": openai_request, "anthropic": anthropic_request}


def verdict(status, body):
    """Maps a response to (ok, message). ok False stops the run."""
    if 200 <= status < 300:
        return True, "ok"
    err = body.get("error") or {}
    code, kind, msg = err.get("code") or "", err.get("type") or "", err.get("message") or ""
    text = f"{status} {code or kind}: {msg}".strip()
    if code == "insufficient_quota" or "credit balance" in msg.lower():
        return False, f"the account is out of credit ({text})"
    if status in (401, 403):
        return False, f"the key was refused ({text})"
    if code == "model_not_found" or kind == "not_found_error" or status == 404:
        return False, f"the model is unknown to this API ({text})"
    if status in (429, 500, 502, 503, 529):
        return True, f"warning, the API is busy right now ({text}); the run retries these"
    return False, f"unexpected answer ({text})"


def check(spec, env=os.environ, send=post):
    """Returns (ok, message) for one provider/model spec."""
    provider, _, model = spec.partition("/")
    build = REQUESTS.get(provider)
    if build is None:
        return True, f"no check for provider {provider!r}, going ahead"
    req, problem = build(model, env)
    if problem:
        return False, problem
    try:
        status, body = send(*req)
    except OSError as e:
        return False, f"could not reach the API ({e})"
    return verdict(status, body)


def main():
    if len(sys.argv) != 2:
        sys.exit(__doc__)
    ok, msg = check(sys.argv[1])
    print(f"preflight {sys.argv[1]}: {msg}", file=sys.stderr)
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
