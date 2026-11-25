AGENT := codeq
include ../common/agent.mk

server: ## Run codeserver on the fixture over stdio, as the ladder does
	@go run ./cmd/codeserver -root testdata
