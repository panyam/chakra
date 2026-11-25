# examples/agents: every agent at once. Each agent's own targets (tui, note,
# walk, exercise, chat, ...) are in its directory: cd codeq && make help.
# LIVE and PROFILE work as they do there.

PROFILE ?= $(HOME)/.agentchat/exercise-agents.json
profile_path = $(abspath $(patsubst ~/%,$(HOME)/%,$(PROFILE)))

help: ## List these targets, and each agent's
	@grep -hE '^[a-zA-Z_-]+:.*## ' Makefile | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'
	@echo; echo "Each agent has its own: cd <agent> && make help. Agents: $(AGENTS)"

AGENTS := $(patsubst %/Makefile,%,$(wildcard */Makefile))

exercise: ## Run every agent's ladder and print its table (LIVE=<connection> CASES=1 QUIET=1)
	@go run ./cmd/exercise $(if $(CASES),-cases) $(if $(QUIET),-quiet) $(if $(LIVE),-live '$(LIVE)' -profile $(profile_path))

profiles: ## List the connections in PROFILE
	@go run ./cmd/exercise -profiles -profile $(profile_path)

test: ## Run every agent's tests
	@go test -race ./...

.DEFAULT_GOAL := help
.PHONY: help exercise profiles test
