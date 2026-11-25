# examples/agents/common/agent.mk
#
# Shared make targets for one agent. An agent's Makefile is two lines:
#
#   AGENT := codeq
#   include ../common/agent.mk
#
# Run them from the agent's directory: make tui, make walk LIVE=haiku, make
# exercise. AGENT defaults to the directory's name.
#
# LIVE names a connection in PROFILE, an agentchat config, which defaults to
# ~/.agentchat/exercise-agents.json. Without LIVE, a target runs on the
# agent's scripted stand-in model.

AGENT   ?= $(notdir $(CURDIR))
PROFILE ?= $(HOME)/.agentchat/exercise-agents.json
AGENT_MK_DIR := $(dir $(lastword $(MAKEFILE_LIST)))
AGENTS_DIR   := $(abspath $(AGENT_MK_DIR)..)
REPO_DIR     := $(abspath $(AGENTS_DIR)/../..)
profile_path  = $(abspath $(patsubst ~/%,$(HOME)/%,$(PROFILE)))
live_flags    = $(if $(LIVE),-live '$(LIVE)' -profile $(profile_path))
walk          = cd $(AGENTS_DIR) && go run ./cmd/walk -agent $(AGENT) $(live_flags)

help: ## List this agent's targets
	@grep -hE '^[a-zA-Z_-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'

tui: ## Walk the ladder in a styled terminal UI (LIVE=<connection> for a real model)
	@$(walk) --tui $(ARGS)

note: ## Walk the ladder in notebook mode: a cell per step, scroll back to compare
	@$(walk) --note $(ARGS)

walk: ## Walk the ladder in plain text, Enter at each step
	@$(walk) $(ARGS)

serve: ## Walk the ladder in a browser on http://localhost:8765
	@$(walk) --serve :8765 $(ARGS)

record: ## Record a full walk on LIVE to walk-<connection>.json, with no prompts
	@[ -n "$(LIVE)" ] || { echo "make record needs LIVE, such as LIVE=haiku"; exit 2; }
	@$(walk) --non-interactive --record $(CURDIR)/walk-$(LIVE).json

walk-md: ## Render walk-<LIVE>.json to WALK.md, with no model calls
	@[ -n "$(LIVE)" ] || { echo "make walk-md needs LIVE, the connection the walk was recorded on"; exit 2; }
	@$(walk) --doc md --from $(CURDIR)/walk-$(LIVE).json > $(CURDIR)/WALK.md
	@echo "wrote WALK.md"

exercise: ## Run the whole ladder and print its table (CASES=1 prints failing cases)
	@cd $(AGENTS_DIR) && go run ./cmd/exercise -agent $(AGENT) $(if $(CASES),-cases) $(if $(QUIET),-quiet) $(live_flags)

chat: ## Chat with the ladder's last step in agentchat (LIVE picks the connection)
	@cfg="$(AGENTS_DIR)/.chat-$(AGENT).json" && \
	  (cd $(AGENTS_DIR) && go run ./cmd/exercise -agent $(AGENT) -chat-config "$$cfg" -profile $(profile_path)) && \
	  echo "agentchat config: $$cfg" && \
	  cd $(REPO_DIR)/surfaces/chat && go run . --config "$$cfg" $(if $(LIVE),--active $(LIVE))

profiles: ## List the connections in PROFILE
	@cd $(AGENTS_DIR) && go run ./cmd/exercise -profiles -profile $(profile_path)

test: ## Run this agent's tests
	@go test -race ./...

.DEFAULT_GOAL := help
.PHONY: help tui note walk serve record walk-md exercise chat profiles test
