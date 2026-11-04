# chakra — agent SDK build and test orchestration.
#
# Every module here is its own Go module, so `go test ./...` at the root only
# covers the root module. Each target below walks MODULES explicitly. That is
# the same reason mcpkit needed per-module targets, and the reason `tidy-all`
# exists: an intra-repo replace means one module's go.sum can drift when a
# sibling changes, and the failure surfaces in the module nobody edited.

MODULES := . host surfaces surfaces/chat surfaces/web \
           store/redis store/gorm \
           ext/checkpoint ext/files ext/exec ext/lsp ext/agentsmd ext/git

# EXAMPLES are built, vetted and tested like MODULES but never tagged, which is
# also what a cross-extension test module (tests/<a>_<b>_e2e) needs.
EXAMPLES := examples/agent-async examples/critic examples/multi-agent examples/host-exercise examples/coding-harness tests/files_git_e2e

# CI runs these same targets rather than restating the module list, so adding a
# module is one edit here. It overrides GOTESTFLAGS only to add -v.
#
# -race is not optional and not per-module. The signal, pool and interruptible
# tests exist to catch races and they live in the root module, which was the one
# place CI ran without it. The whole tree costs about 100s under -race.
#
# The timeout is a diagnostic, not a budget: past it, a hang arrives as a
# goroutine dump naming the blocked call instead of a bare line at the 600s
# default. 300s leaves headroom over ext/lsp, the slowest module at ~55s under
# -race, whose stub language server is this test binary re-executed. Its
# lsp_live tag drives a real gopls and is deliberately not wired into CI.
#
# ext/exec is the other module worth knowing about here: its commands are also
# this test binary re-executed, so nothing needs installing. Its exec_live tag
# runs the same surface under a real sandbox-exec and is darwin-only, so no
# sandbox backend is exercised on a Linux runner. The tests name Unconfined
# explicitly and the platform's own backend refuses. See the module README.
GOTESTFLAGS ?= -race -timeout 300s

.PHONY: help build test test-examples test-web test-web-e2e testall vet fmt check-fmt cover tidy-all sync-mcpkit bump-siblings tag tag-push pg dogfood dogfood-web coding-harness-exercise coding-harness-exercise-live coding-harness-web coding-harness-parity tbench-build tbench tbench-report tbench-ingest tbench-matrix tbench-stale deploy-web weblogs \
        check-no-binaries check-ext-isolation check-dep-consistency setup-hooks

help: ## Show this help
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

build: ## Build every module
	@for m in $(MODULES) $(EXAMPLES); do \
		echo "==> build $$m"; \
		(cd $$m && go build ./...) || exit 1; \
	done

test: ## Test every module (not the examples)
	@for m in $(MODULES); do \
		echo "==> test $$m"; \
		(cd $$m && go test ./... $(GOTESTFLAGS)) || exit 1; \
	done

test-examples: ## Test the SDK examples (and the host exercise)
	@for m in $(EXAMPLES); do \
		echo "==> test $$m"; \
		(cd $$m && go test ./... $(GOTESTFLAGS)) || exit 1; \
	done
	@echo "==> test examples/terminal-bench and scripts (python)"
	@cd examples/terminal-bench && python3 -m unittest -q test_chakra_config
	@python3 -m unittest discover -q -s scripts -p 'test_*.py'

testall: test test-examples check-fmt check-no-binaries check-ext-isolation check-dep-consistency ## Everything CI runs

vet: ## go vet every module
	@for m in $(MODULES) $(EXAMPLES); do \
		echo "==> vet $$m"; \
		(cd $$m && go vet ./...) || exit 1; \
	done

fmt: ## gofmt every module
	@gofmt -l -w $$(git ls-files '*.go')

check-fmt: ## Fail if any Go file (tracked, or new and not ignored) is not gofmt-clean
	@out=$$(gofmt -l $$(git ls-files --cached --others --exclude-standard '*.go')); \
	if [ -n "$$out" ]; then \
		echo "not gofmt-clean, run 'make fmt':"; echo "$$out"; exit 1; \
	fi

test-web: ## Run the web frontend's unit tests (vitest; needs Node)
	cd surfaces/web/web && npm ci --silent && npm test

# Playwright drives the page the Go binary serves (the committed bundle) over
# internal/e2eserver's stub connections, so it needs Go and the mcpkit checkout
# beside this one, as well as Node.
test-web-e2e: ## Run the web frontend's browser checks (Playwright; needs Node and Go)
	cd surfaces/web/web && npm ci --silent && npx playwright install --with-deps chromium && npm run e2e

tidy-all: ## go mod tidy every module. Required after touching a shared import.
	@for m in $(MODULES) $(EXAMPLES); do \
		echo "==> tidy $$m"; \
		(cd $$m && go mod tidy) || exit 1; \
	done

# Bring every module up to date with ../mcpkit/main (#73). A tidy alone raises
# only the modules that import the newer code and leaves the rest split, which
# check-dep-consistency then fails on, so the lagging pins are raised and the
# tree tidied again. .github/workflows/tidy.yml runs this and opens the PR.
sync-mcpkit: ## tidy-all, then raise lagging pins so no dependency is split
	@$(MAKE) -s tidy-all
	@python3 scripts/check_dep_consistency.py --fix
	@$(MAKE) -s tidy-all
	@python3 scripts/check_dep_consistency.py

check-no-binaries: ## Fail if a compiled executable is tracked
	@sh scripts/check-no-binaries.sh

check-ext-isolation: ## Fail if one ext/ module directly requires another
	@sh scripts/check-ext-isolation.sh

check-dep-consistency: ## Fail if a third-party dep is pinned at two versions across modules
	@python3 scripts/check_dep_consistency.py

cover: ## Coverage across the root module
	@go test ./... -coverprofile=coverage.out && go tool cover -func=coverage.out | tail -1

pg: ## Playground: boot a demo MCP server + launch agentchat's TUI (needs a local OpenAI-compatible model)
	@bash scripts/playground.sh

# Mission #89's exercise: a host.App against a demo server that declares
# instructions, skills, an event hint and an agent, with a scripted model, on
# both client wires. No model or key needed. It exits non-zero only when a
# result differs from examples/host-exercise/expected.go.
host-exercise: ## Mission #89 exercise: PASS/FAIL per host obligation, both wires (no model needed)
	@cd examples/host-exercise && go run .

# HOST_EXERCISE_ACTIVE picks the connection from examples/host-exercise/agentchat.json.
HOST_EXERCISE_ACTIVE ?= deepseek

host-exercise-live: ## The same demo server, with agentchat and a real model (HOST_EXERCISE_ACTIVE=claude etc.)
	@HOST_EXERCISE_ACTIVE=$(HOST_EXERCISE_ACTIVE) bash scripts/host-exercise-live.sh

# The mission exercise (issue 57): agentchat as a coding agent on this repo,
# with the file tools, checkpoint, exec allowlist and gopls from
# examples/dogfood. Snapshots and sessions live outside the tree, so the agent
# cannot read or edit its own undo history.
DOGFOOD_DIR ?= $(HOME)/.agentchat/dogfood

# DOGFOOD_ACTIVE picks the starting connection from examples/dogfood/config.json
# (claude, deepseek, openrouter, local); empty keeps the config's own.
DOGFOOD_ACTIVE ?=
# DOGFOOD_UI is agentchat's --ui: tui, notebook or plain (line-based, no TTY tricks).
DOGFOOD_UI ?= tui

dogfood: ## Mission exercise: agentchat on this repo with files, exec, LSP (needs gopls and a model; DOGFOOD_ACTIVE=deepseek etc.)
	@command -v gopls >/dev/null || { echo "make dogfood needs gopls on PATH: go install golang.org/x/tools/gopls@latest"; exit 1; }
	@[ -n "$(DOGFOOD_ACTIVE)" ] || [ -n "$$ANTHROPIC_API_KEY" ] || echo "note: ANTHROPIC_API_KEY is unset; pick another connection with DOGFOOD_ACTIVE=deepseek (or openrouter, local)"
	@mkdir -p $(DOGFOOD_DIR)
	cd surfaces/chat && go run . \
		--config $(CURDIR)/examples/dogfood/config.json \
		--workspace $(CURDIR) \
		--workspace-config $(CURDIR)/examples/dogfood/workspace.json \
		--checkpoint-store $(DOGFOOD_DIR)/checkpoints \
		--session-store sqlite://$(DOGFOOD_DIR)/sessions.db \
		$(if $(DOGFOOD_ACTIVE),--active $(DOGFOOD_ACTIVE)) \
		--ui $(DOGFOOD_UI)

# The same exercise in the browser, which is where the mission runs while the
# TUI is parked (#66). Shares the config, connections and store with dogfood.
DOGFOOD_ADDR ?= localhost:8090

dogfood-web: ## Mission exercise in the browser: agentweb on this repo (open http://$(DOGFOOD_ADDR)/)
	@command -v gopls >/dev/null || { echo "make dogfood-web needs gopls on PATH: go install golang.org/x/tools/gopls@latest"; exit 1; }
	@[ -n "$(DOGFOOD_ACTIVE)" ] || [ -n "$$ANTHROPIC_API_KEY" ] || echo "note: ANTHROPIC_API_KEY is unset; pick another connection with DOGFOOD_ACTIVE=deepseek (or openrouter, local)"
	@mkdir -p $(DOGFOOD_DIR)
	cd surfaces/web && go run ./cmd/agentweb \
		--addr $(DOGFOOD_ADDR) \
		--config $(CURDIR)/examples/dogfood/config.json \
		--workspace $(CURDIR) \
		--workspace-config $(CURDIR)/examples/dogfood/workspace.json \
		--checkpoint-store $(DOGFOOD_DIR)/checkpoints \
		--session-store sqlite://$(DOGFOOD_DIR)/sessions.db \
		$(if $(DOGFOOD_ACTIVE),--active $(DOGFOOD_ACTIVE))

# Mission #162's exercise: chakra's coding agent (agentweb over the profile in
# examples/coding-harness) fixes a failing test in a fixture repo, driven over
# agentweb's Connect API. Scripted model, no key; exits non-zero only when a
# result differs from examples/coding-harness/expected.go.
coding-harness-exercise: ## Mission #162 exercise: PASS/FAIL per coding-agent behaviour (no model needed)
	@cd examples/coding-harness && go run .

# CH_ACTIVE picks the connection from examples/coding-harness/config.json;
# CH_MODEL overrides its model.
CH_ACTIVE ?= openai
CH_MODEL ?=
# CH_INSTRUCTIONS picks how AGENTS.md and the like load: chakra, codex or claude.
CH_INSTRUCTIONS ?=
# CH_ROUTE_TO sends steps from CH_ROUTE_FROM (default 2) on to another
# connection, CH_ROUTE_MODEL overriding its model (#185).
CH_ROUTE_TO ?=
CH_ROUTE_FROM ?= 2
CH_ROUTE_MODEL ?=

coding-harness-exercise-live: ## The same exercise with a real model, plus a metrics line (CH_ACTIVE=claude, CH_MODEL=gpt-5.4, CH_ROUTE_TO=openai CH_ROUTE_MODEL=gpt-5.4-mini etc.)
	@cd examples/coding-harness && go run . --live --active $(CH_ACTIVE) $(if $(CH_MODEL),--model $(CH_MODEL)) $(if $(CH_INSTRUCTIONS),--instructions $(CH_INSTRUCTIONS)) $(if $(CH_ROUTE_TO),--route-to $(CH_ROUTE_TO) --route-from $(CH_ROUTE_FROM)) $(if $(CH_ROUTE_MODEL),--route-model $(CH_ROUTE_MODEL))

CH_ADDR ?= localhost:8091

coding-harness-web: ## agentweb with chat at / and the coding agent at /code/, on a fresh fixture copy per session (CH_WORKSPACE=dir, CH_INSTRUCTIONS=codex)
	@CH_ADDR=$(CH_ADDR) CH_ACTIVE=$(CH_ACTIVE) CH_MODEL=$(CH_MODEL) CH_WORKSPACE=$(CH_WORKSPACE) CH_INSTRUCTIONS=$(CH_INSTRUCTIONS) bash scripts/coding-harness-web.sh

# Terminal-Bench 2 through Harbor, its official harness (#195). TB_AGENTS run
# one after another on the same tasks; chakra goes in through
# examples/terminal-bench/chakra_agent.py, codex and claude-code are Harbor's
# built-in agents. Needs Docker, harbor on PATH, and a checkout of
# terminal-bench-2 at TB_PATH. TB_SET picks the tasks: starter (the default) or
# hard, eight of the tasks the dataset marks hard that need no GPU and fit a 30
# minute agent budget. TB_TASKS overrides the set, and TB_TASKS= (empty) runs
# all 89. TB_EFFORT pins reasoning effort on every agent through Harbor's
# reasoning_effort option. TB_EFFORT= (empty) leaves each agent at its own
# default, which is how runs before this option were made.
TB_PATH ?= $(HOME)/newstack/chakra/benchmarks/terminal-bench-2
TB_JOBS ?= $(HOME)/newstack/chakra/benchmarks/jobs
TB_AGENTS ?= chakra codex
TB_MODEL ?= openai/gpt-5.5
TB_K ?= 1
TB_CONCURRENT ?= 2
TB_EFFORT ?= medium
TB_SET ?= starter
TB_TASKS_STARTER := fix-git prove-plus-comm cobol-modernization kv-store-grpc polyglot-c-py \
            nginx-request-logging openssl-selfsigned-cert custom-memory-heap-crash \
            sanitize-git-repo multi-source-data-merger
TB_TASKS_HARD := cancel-async-tasks configure-git-webserver fix-code-vulnerability polyglot-rust-c \
            password-recovery sparql-university write-compressor make-mips-interpreter
TB_TASKS ?= $(if $(filter hard,$(TB_SET)),$(TB_TASKS_HARD),$(TB_TASKS_STARTER))
# The matrix columns `make tbench` would fill with the current TB_AGENTS,
# TB_MODEL and TB_EFFORT. tbench-stale picks cells from these only.
TB_SETUPS = $(foreach a,$(TB_AGENTS),$(a)/$(subst /,-,$(TB_MODEL))/$(or $(TB_EFFORT),default))
N ?= 10
BUDGET ?= 5

tbench-build: ## Static Linux agentchat for the Terminal-Bench containers (amd64, as the task images are)
	@mkdir -p examples/terminal-bench/bin
	@cd surfaces/chat && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ../../examples/terminal-bench/bin/agentchat-linux-amd64 .
	@echo "$$(git rev-parse --short=12 HEAD)$$(git diff --quiet HEAD -- . || echo -dirty)" > examples/terminal-bench/bin/VERSION

tbench: tbench-build ## Terminal-Bench 2 via Harbor: TB_AGENTS on TB_SET or TB_TASKS, TB_K attempts each (TB_MODEL=openai/gpt-5.5, TB_EFFORT=medium)
	@for a in $(TB_AGENTS); do \
		case $$a in chakra) agent=chakra_agent:ChakraAgent ;; *) agent=$$a ;; esac; \
		echo "==> $$a"; \
		PYTHONPATH=$(CURDIR)/examples/terminal-bench harbor run -p $(TB_PATH) -a $$agent -m $(TB_MODEL) \
			$(if $(TB_EFFORT),--ak reasoning_effort=$(TB_EFFORT)) -k $(TB_K) -n $(TB_CONCURRENT) -o $(TB_JOBS) --job-name tbench-$$a-$$(date +%Y%m%d-%H%M%S) \
			$(foreach t,$(TB_TASKS),-i $(t)) -y || echo "$$a: harbor exited non-zero, carrying on"; \
	done

tbench-report: ## Side-by-side markdown of the newest tbench job per agent in TB_JOBS
	@python3 scripts/tbench_report.py $(TB_JOBS)

tbench-ingest: ## Copy new Harbor jobs in TB_JOBS into benchmarks/results/, one JSON per job (safe to rerun)
	@python3 scripts/tbench_matrix.py ingest $(TB_JOBS)

tbench-matrix: ## Task x setup matrix of last runs from benchmarks/results/, as markdown and benchmarks/matrix/index.html
	@python3 scripts/tbench_matrix.py render $(TB_PATH)

tbench-stale: ## The N oldest or never-run cells of TB_SETUPS, up to BUDGET dollars at list price (N=10 BUDGET=5); RUN=1 runs them
	@mkdir -p benchmarks/matrix && python3 scripts/tbench_matrix.py stale $(TB_PATH) $(N) $(BUDGET) --setups "$(TB_SETUPS)" > benchmarks/matrix/stale-picks
	@cat benchmarks/matrix/stale-picks
	@$(if $(RUN),while read -r cmd; do eval "$(MAKE) $${cmd#make }" || exit 1; done < benchmarks/matrix/stale-picks && $(MAKE) tbench-ingest)

coding-harness-parity: ## codex exec vs the coding harness, same fixture and model, side by side (needs codex and OPENAI_API_KEY; PARITY_MODEL=gpt-5.5, PARITY_ACTIVE=openai, PARITY_INSTRUCTIONS=codex)
	@bash scripts/coding-harness-parity.sh

# ---------------------------------------------------------------------------
# Release
#
# Every module is tagged at the same version, so `go get
# github.com/panyam/chakra/<module>@vX.Y.Z` resolves consistently. That matters
# more here than it looks: the modules require each other, and Go ignores
# `replace` outside the main module, so a sibling pinned at a version that was
# never tagged makes the whole tree unresolvable from outside. That was the
# state this repo inherited from mcpkit, deliberately, and `bump-siblings` is
# what keeps it from coming back.
# ---------------------------------------------------------------------------

bump-siblings: ## Repoint every intra-repo require at a version (usage: make bump-siblings V=v0.1.0)
	@if [ -z "$(V)" ]; then echo "Usage: make bump-siblings V=v0.1.0"; exit 1; fi
	@for m in $(MODULES) $(EXAMPLES); do \
		for dep in $(MODULES); do \
			[ "$$dep" = "." ] && path=github.com/panyam/chakra || path=github.com/panyam/chakra/$$dep; \
			grep -q "$$path v" $$m/go.mod 2>/dev/null || continue; \
			(cd $$m && go mod edit -require=$$path@$(V)) || exit 1; \
		done; \
	done
	@echo "==> intra-repo requires repointed at $(V)"
	@$(MAKE) -s tidy-all

tag: ## Tag every module (usage: make tag V=v0.1.0)
	@if [ -z "$(V)" ]; then echo "Usage: make tag V=v0.1.0"; exit 1; fi
	@echo "Tagging $(V) across all modules..."
	git tag -a $(V) -m "$(V)"
	@for mod in $(MODULES); do \
		[ "$$mod" = "." ] && continue; \
		echo "  $$mod/$(V)"; \
		git tag -a $$mod/$(V) -m "$$mod/$(V)"; \
	done

tag-push: ## Tag and push in one step (usage: make tag-push V=v0.1.0)
	@$(MAKE) tag V=$(V)
	git push origin $(V) $$(echo '$(MODULES)' | tr ' ' '\n' | grep -v '^\.$$' | sed 's|$$|/$(V)|' | tr '\n' ' ')

setup-hooks: ## Install the local pre-commit and pre-push hooks
	@cp scripts/pre-commit-hook.sh .git/hooks/pre-commit && chmod +x .git/hooks/pre-commit
	@cp scripts/pre-push-hook.sh .git/hooks/pre-push && chmod +x .git/hooks/pre-push
	@echo "pre-commit + pre-push hooks installed."

# App Engine project agentweb deploys to (https://vibestitch.appspot.com).
GCP_PROJECT ?= vibestitch

# Deploy agentweb to App Engine, after the web tests and a fresh bundle.
# surfaces/web/deploy.sh does the deploy itself (vendoring, the access token);
# surfaces/web/app.yaml has the instance settings.
deploy-web: test-web ## Deploy agentweb to App Engine (GCP_PROJECT=vibestitch)
	cd surfaces/web/web && npm run build
	cd surfaces/web && go test ./... $(GOTESTFLAGS)
	surfaces/web/deploy.sh $(GCP_PROJECT)

weblogs: ## Tail agentweb's App Engine logs
	gcloud app logs tail -s default --project $(GCP_PROJECT)
