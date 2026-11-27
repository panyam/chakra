AGENT := copilot
include ../common/agent.mk

web: ## Serve the board in a browser on http://localhost:8781, with the agent on LIVE
	@[ -n "$(LIVE)" ] || { echo "make web needs LIVE, such as LIVE=haiku"; exit 2; }
	@echo "open http://localhost:8781/ once it says it's serving"
	@cd web && go run . -live '$(LIVE)' -profile $(profile_path)

.PHONY: web
