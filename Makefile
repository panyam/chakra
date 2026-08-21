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

.PHONY: help build test test-examples testall vet fmt tidy-all check-no-binaries check-ext-isolation setup-hooks

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
		(cd $$m && go test ./... -timeout 180s) || exit 1; \
	done

test-examples: ## Test the three SDK examples
	@for m in $(EXAMPLES); do \
		echo "==> test $$m"; \
		(cd $$m && go test ./... -timeout 180s) || exit 1; \
	done

testall: test test-examples check-no-binaries check-ext-isolation ## Everything CI runs

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

setup-hooks: ## Install the local pre-commit hook
	@cp scripts/pre-commit-hook.sh .git/hooks/pre-commit && chmod +x .git/hooks/pre-commit
	@echo "pre-commit hook installed."
