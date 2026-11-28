module github.com/panyam/chakra/examples/agents

go 1.26.11

toolchain go1.27.4

require (
	github.com/panyam/chakra v0.3.10
	github.com/panyam/chakra/host v0.3.10
	github.com/panyam/demokit v0.0.33
	github.com/panyam/mcpkit v0.8.23
)

require (
	github.com/alexedwards/scs/v2 v2.9.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/creack/pty v1.1.24 // indirect
	github.com/fernet/fernet-go v0.0.0-20240119011108-303da6aec611 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/panyam/mcpkit/experimental/ext/agents v0.8.23 // indirect
	github.com/panyam/mcpkit/experimental/ext/agents/clients/go v0.8.20 // indirect
	github.com/panyam/mcpkit/experimental/ext/events v0.8.23 // indirect
	github.com/panyam/mcpkit/experimental/ext/events/clients/go v0.8.23 // indirect
	github.com/panyam/mcpkit/ext/auth v0.8.20 // indirect
	github.com/panyam/mcpkit/ext/skills v0.8.20 // indirect
	github.com/panyam/oneauth v0.3.19 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/oauth2 v0.37.0 // indirect
)

require (
	charm.land/lipgloss/v2 v2.0.3 // indirect
	github.com/atotto/clipboard v0.1.4 // indirect
	github.com/aymanbagabas/go-osc52/v2 v2.0.1 // indirect
	github.com/bahlo/generic-list-go v0.2.0 // indirect
	github.com/buger/jsonparser v1.6.1 // indirect
	github.com/charmbracelet/bubbles v1.0.0 // indirect
	github.com/charmbracelet/bubbletea v1.3.10 // indirect
	github.com/charmbracelet/colorprofile v0.4.3 // indirect
	github.com/charmbracelet/lipgloss v1.1.1-0.20250404203927-76690c660834 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20251205161215-1948445e3318 // indirect
	github.com/charmbracelet/x/ansi v0.11.7 // indirect
	github.com/charmbracelet/x/cellbuf v0.0.15 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/erikgeiser/coninput v0.0.0-20211004153227-1c3628e74d0f // indirect
	github.com/gorilla/websocket v1.5.4-0.20240701034025-d67f41855da4 // indirect
	github.com/invopop/jsonschema v0.14.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/mattn/go-localereader v0.0.1 // indirect
	github.com/mattn/go-runewidth v0.0.23 // indirect
	github.com/muesli/ansi v0.0.0-20230316100256-276c6243b2f6 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/muesli/termenv v0.16.0 // indirect
	github.com/panyam/chakra/ext/exec v0.3.10
	github.com/panyam/chakra/ext/files v0.3.10
	github.com/panyam/demokit/notebook v0.0.33 // indirect
	github.com/panyam/gocurrent v0.1.4 // indirect
	github.com/panyam/goutils v0.1.13 // indirect
	github.com/panyam/servicekit v0.2.5 // indirect
	github.com/panyam/templar v0.1.0 // indirect
	github.com/pb33f/ordered-map/v2 v2.3.1 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	github.com/yuin/goldmark v1.8.2 // indirect
	go.yaml.in/yaml/v4 v4.0.0-rc.6 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.16.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/panyam/chakra => ../..

replace github.com/panyam/mcpkit => ../../../../mcpkit/main

replace github.com/panyam/chakra/ext/files => ../../ext/files

replace github.com/panyam/chakra/ext/exec => ../../ext/exec

replace github.com/panyam/chakra/host => ../../host
