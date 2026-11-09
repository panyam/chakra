module github.com/panyam/chakra/tests/chat_element_rooms_e2e

go 1.26.9

replace github.com/panyam/chakra => ../..

replace github.com/panyam/chakra/host => ../../host

replace github.com/panyam/chakra/surfaces => ../../surfaces

replace github.com/panyam/chakra/surfaces/web => ../../surfaces/web

replace github.com/panyam/mcpkit => ../../../../mcpkit/main

replace github.com/panyam/mcpkit/experimental/ext/agents => ../../../../mcpkit/main/experimental/ext/agents

replace github.com/panyam/mcpkit/experimental/ext/agents/clients/go => ../../../../mcpkit/main/experimental/ext/agents/clients/go

replace github.com/panyam/mcpkit/experimental/ext/events => ../../../../mcpkit/main/experimental/ext/events

replace github.com/panyam/mcpkit/experimental/ext/events/clients/go => ../../../../mcpkit/main/experimental/ext/events/clients/go

replace github.com/panyam/mcpkit/experimental/ext/rooms => ../../../../mcpkit/main/experimental/ext/rooms

replace github.com/panyam/mcpkit/experimental/ext/rooms/massrelay => ../../../../mcpkit/main/experimental/ext/rooms/massrelay

replace github.com/panyam/mcpkit/ext/auth => ../../../../mcpkit/main/ext/auth

replace github.com/panyam/mcpkit/ext/skills => ../../../../mcpkit/main/ext/skills

replace github.com/panyam/mcpkit/ext/tasks => ../../../../mcpkit/main/ext/tasks

require (
	github.com/panyam/chakra v0.3.4
	github.com/panyam/chakra/host v0.3.4
	github.com/panyam/chakra/surfaces/web v0.3.4
	github.com/panyam/mcpkit v0.8.20
	github.com/panyam/mcpkit/experimental/ext/rooms v0.8.20
	github.com/panyam/mcpkit/experimental/ext/rooms/massrelay v0.8.20
	github.com/panyam/servicekit v0.2.2
)

require (
	connectrpc.com/connect v1.19.2 // indirect
	github.com/alexedwards/scs/v2 v2.9.0 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/buger/jsonparser v1.6.1 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/fernet/fernet-go v0.0.0-20240119011108-303da6aec611 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.30.0 // indirect
	github.com/invopop/jsonschema v0.14.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/panyam/gocurrent v0.1.2 // indirect
	github.com/panyam/goutils v0.1.13 // indirect
	github.com/panyam/massrelay v0.3.7 // indirect
	github.com/panyam/mcpkit/experimental/ext/agents v0.8.20 // indirect
	github.com/panyam/mcpkit/experimental/ext/agents/clients/go v0.8.20 // indirect
	github.com/panyam/mcpkit/experimental/ext/events v0.8.20 // indirect
	github.com/panyam/mcpkit/experimental/ext/events/clients/go v0.8.20 // indirect
	github.com/panyam/mcpkit/ext/auth v0.8.20 // indirect
	github.com/panyam/mcpkit/ext/skills v0.8.20 // indirect
	github.com/panyam/oneauth v0.3.2 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.1 // indirect
	github.com/prometheus/client_golang v1.24.1 // indirect
	github.com/prometheus/client_model v0.6.3 // indirect
	github.com/prometheus/common v0.72.0 // indirect
	github.com/prometheus/otlptranslator v1.0.0 // indirect
	github.com/prometheus/procfs v0.22.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/prometheus v0.69.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	go.opentelemetry.io/proto/otlp v1.11.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.6 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/oauth2 v0.37.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.16.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260928230214-8a89bd6388cc // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260928230214-8a89bd6388cc // indirect
	google.golang.org/grpc v1.84.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/panyam/chakra/store/redis => ../../store/redis

replace github.com/panyam/chakra/store/gorm => ../../store/gorm

replace github.com/panyam/chakra/ext/files => ../../ext/files

replace github.com/panyam/chakra/ext/checkpoint => ../../ext/checkpoint

replace github.com/panyam/chakra/ext/lsp => ../../ext/lsp

replace github.com/panyam/chakra/ext/exec => ../../ext/exec

replace github.com/panyam/chakra/ext/git => ../../ext/git

replace github.com/panyam/chakra/ext/agentsmd => ../../ext/agentsmd

replace github.com/panyam/chakra/provider/anthropic => ../../provider/anthropic
