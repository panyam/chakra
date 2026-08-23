# chakra — agent SDK build and test orchestration.
#
# Every module here is its own Go module, so `go test ./...` at the root only
# covers the root module. Each target below walks MODULES explicitly. That is
# the same reason mcpkit needed per-module targets, and the reason `tidy-all`
# exists: an intra-repo replace means one module's go.sum can drift when a
# sibling changes, and the failure surfaces in the module nobody edited.

MODULES := . host surfaces surfaces/chat surfaces/web \
           store/redis store/gorm \
           ext/checkpoint ext/files ext/exec ext/lsp

EXAMPLES := examples/agent-async examples/critic examples/multi-agent

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

.PHONY: help build test test-examples testall vet fmt cover tidy-all bump-siblings tag tag-push pg \
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

test-examples: ## Test the three SDK examples
	@for m in $(EXAMPLES); do \
		echo "==> test $$m"; \
		(cd $$m && go test ./... $(GOTESTFLAGS)) || exit 1; \
	done

testall: test test-examples check-no-binaries check-ext-isolation check-dep-consistency ## Everything CI runs

vet: ## go vet every module
	@for m in $(MODULES) $(EXAMPLES); do \
		echo "==> vet $$m"; \
		(cd $$m && go vet ./...) || exit 1; \
	done

fmt: ## gofmt every module
	@gofmt -l -w $$(git ls-files '*.go')

tidy-all: ## go mod tidy every module. Required after touching a shared import.
	@for m in $(MODULES) $(EXAMPLES); do \
		echo "==> tidy $$m"; \
		(cd $$m && go mod tidy) || exit 1; \
	done

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
