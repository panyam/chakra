// Adapted (copied, not imported) from mcpkit's examples/common/otel, per
// the adopt-do-not-depend decision. examples/common is example scaffolding
// with its own module and replace lattice. A product CLI that depended on
// it would invert that relationship. The four-exporter contract (issue
// 666) is kept verbatim. If a third consumer appears, this code moves to a
// shared home. The examples, `make demo` and the docker/observability/
// stack named below are all in mcpkit.
// This file ships SetupTelemetry. It is env-gated TracerProvider
// wiring, and every mcpkit example uses it to get a uniform, opt-in
// observability surface (issue 666).
//
// Every example follows this contract.
//
//	tp, shutdown, err := SetupTelemetry(ctx,
//	    WithServiceName("my-example"),
//	    WithExporter(*exporterFlag),
//	    WithOTLPEndpoint(*otlpEndpointFlag),
//	)
//	if err != nil { log.Fatalf(...) }
//	defer shutdown(context.Background())
//
//	common.RunServer(common.ServerConfig{
//	    ...
//	    TracerProvider: tp,
//	})
//
// Decision matrix (Exporter selector × env):
//
//	Exporter==""       → core.NoopTracerProvider{} + no-op shutdown.
//	                     Zero allocations, no SDK pulled at runtime.
//	                     This is the DEFAULT. Operators opt in per
//	                     example via --exporter or EXPORTER=.
//	Exporter=="stdout" → stdouttrace exporter (sync); every span
//	                     dumps to the supplied writer (os.Stdout by
//	                     default). Teaching / demo mode.
//	Exporter=="otlp"   → otlptracegrpc exporter (sync) to OTLPEndpoint
//	                     (default localhost:4317, matches the
//	                     docker/observability/ stack). Honors
//	                     OTEL_EXPORTER_OTLP_ENDPOINT as a fallback.
//	Exporter=="auto"   → probe the OTLP endpoint; if reachable, behave
//	                     like "otlp"; if not, silently fall back to
//	                     Noop. The "best effort, don't complain"
//	                     mode for examples that may or may not have
//	                     the observability stack running. Unlike
//	                     "otlp" mode, unreachable endpoints emit NO
//	                     warning, since with auto the operator
//	                     accepted maybe-on-maybe-off semantics.
//
// In explicit "otlp" mode, a dial or construction failure logs a
// warning and falls back to Noop. A dead observability stack should
// never break `make demo`.
//
// The standard OTel env vars are fallbacks under the explicit options.
// They are OTEL_SERVICE_NAME, OTEL_EXPORTER_OTLP_ENDPOINT and
// OTEL_RESOURCE_ATTRIBUTES. Explicit code beats spooky env, but env
// still works for `make demo` orchestration.
//
// mcpotel.NewProvider already wraps the returned core.TracerProvider.
// So the caller hands it directly to server.WithTracerProvider or
// client.WithTracerProvider, with no adapter wiring at the call site.

package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/panyam/mcpkit/core"
	mcpotel "github.com/panyam/mcpkit/ext/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// otlpProbeTimeout caps how long SetupTelemetry waits on a TCP
// connect attempt to the OTLP endpoint. After that it declares the
// stack unreachable and falls back to Noop. The timeout is generous
// enough for a container that is starting up. It is short enough that
// a wholly-absent stack doesn't visibly stall `make demo`.
const otlpProbeTimeout = 500 * time.Millisecond

// Exporter mode constants. Examples accept these strings on
// --exporter and EXPORTER=.
const (
	ExporterStdout = "stdout"
	ExporterOTLP   = "otlp"
	// ExporterAuto means "best-effort OTLP". It probes the
	// configured OTLP endpoint. If the endpoint answers, traces
	// ship. If not, the helper silently returns Noop with no
	// warning, because the operator opted into maybe-on-maybe-off
	// semantics. Pick it when you want telemetry while the local
	// docker/observability/ stack is up, and don't want a missing
	// stack to be noisy.
	ExporterAuto = "auto"

	// DefaultOTLPEndpoint matches the gRPC receiver port of the
	// docker/observability/ stack. The OTel Go SDK's own default
	// endpoint convention is also :4317. So this default has the
	// lowest friction for the local docker stack and for any other
	// OTLP receiver an operator may run.
	DefaultOTLPEndpoint = "localhost:4317"
)

// ShutdownFunc flushes any buffered spans and tears down the
// exporter pipeline. SetupTelemetry returns one, and callers MUST
// defer it. Pass context.Background(), typically. Cancelling the
// original ctx often triggers the shutdown, so the shutdown path
// shouldn't honor that cancellation.
//
// The function is a no-op when Exporter=="" or when an OTLP dial
// fell back to Noop. Calling it twice is safe.
type ShutdownFunc func(context.Context) error

// SetupOption configures the SetupTelemetry call. Apply it via the
// With... helpers. When two options set the same field, the later
// one wins.
type SetupOption func(*setupConfig)

type setupConfig struct {
	serviceName         string
	exporter            string
	otlpEndpoint        string
	stdoutWriter        *os.File
	resourceAttr        map[string]string
	instrumentationName string
}

// WithServiceName sets the OTel Resource service.name attribute. If
// unset, OTEL_SERVICE_NAME is consulted. If that is also empty, the
// SDK default (`unknown_service:<binary>`) applies. Every example
// SHOULD pass this, so traces appear under a recognizable name in
// Grafana / Tempo / Jaeger.
func WithServiceName(name string) SetupOption {
	return func(c *setupConfig) {
		if name != "" {
			c.serviceName = name
		}
	}
}

// WithExporter picks the exporter mode, one of "", "stdout", "otlp"
// or "auto". Empty string → Noop (the default). "auto" probes the
// OTLP endpoint and falls back to Noop silently if it is
// unreachable. Pick it for examples that may or may not have the
// local observability stack running. An unknown value makes
// SetupTelemetry return an error, so a typo doesn't silently turn
// telemetry off.
func WithExporter(name string) SetupOption {
	return func(c *setupConfig) {
		c.exporter = name
	}
}

// WithOTLPEndpoint overrides the OTLP gRPC endpoint. When unset,
// OTEL_EXPORTER_OTLP_ENDPOINT is consulted; if that is also empty,
// DefaultOTLPEndpoint (localhost:4317) is used. Only relevant when
// Exporter is "otlp" or "auto", since "auto" probes this endpoint too.
func WithOTLPEndpoint(endpoint string) SetupOption {
	return func(c *setupConfig) {
		if endpoint != "" {
			c.otlpEndpoint = endpoint
		}
	}
}

// WithStdoutWriter overrides the writer for Exporter=="stdout"
// mode. Default os.Stdout. It is mostly useful in tests that assert
// on the rendered span output.
func WithStdoutWriter(w *os.File) SetupOption {
	return func(c *setupConfig) {
		if w != nil {
			c.stdoutWriter = w
		}
	}
}

// WithInstrumentationName overrides the OTel instrumentation library
// name for mcpotel.NewProvider. It defaults to mcpotel's own default
// (`github.com/panyam/mcpkit/server`). Client-side wiring typically
// passes `github.com/panyam/mcpkit/client`, so observability backends
// group spans by emitting library.
//
// Empty name is a no-op (falls through to mcpotel's default).
func WithInstrumentationName(name string) SetupOption {
	return func(c *setupConfig) {
		if name != "" {
			c.instrumentationName = name
		}
	}
}

// WithResourceAttr layers an additional OTel Resource attribute on
// top of service.name. Use it for deployment.environment, replica id,
// tenant, or any other dimension the operator wants in Grafana
// filters. It may be called multiple times, and the last value wins
// per key.
//
// This option takes precedence when OTEL_RESOURCE_ATTRIBUTES env sets
// the same key. The Noop path (Exporter=="") has no Resource, so it
// discards these attributes.
func WithResourceAttr(key, value string) SetupOption {
	return func(c *setupConfig) {
		if c.resourceAttr == nil {
			c.resourceAttr = map[string]string{}
		}
		c.resourceAttr[key] = value
	}
}

// ClientInstrumentationName is the OTel instrumentation library
// label for client-side spans from SetupClientTelemetry. The
// otel/stdout walkthrough in mcpkit PR 680 used the same value. So
// client/server spans group correctly in OTel-aware backends
// (Jaeger's "Library" column, Tempo's instrumentation filter).
const ClientInstrumentationName = "github.com/panyam/mcpkit/client"

// SetupClientTelemetry is SetupTelemetry pre-configured for the
// host (client) side of an example walkthrough. It is equivalent to
// SetupTelemetry(ctx, opts..., WithInstrumentationName(ClientInstrumentationName)).
// The server and client wirings differ only in the
// instrumentation-name preset. So this saves every walkthrough from
// typing the magic string. An explicit WithInstrumentationName(...)
// in opts still wins, since the last option applies.
//
// Pair the returned core.TracerProvider with client.WithTracerProvider
// at the client.NewClient call site.
//
//	tp, shutdown, err := SetupClientTelemetry(ctx,
//	    WithExporter(tel.Exporter),
//	    WithOTLPEndpoint(tel.OTLPEndpoint),
//	    WithServiceName("my-example-host"),
//	)
//	defer shutdown(context.Background())
//	c := client.NewClient(url, info, client.WithTracerProvider(tp))
func SetupClientTelemetry(ctx context.Context, opts ...SetupOption) (core.TracerProvider, ShutdownFunc, error) {
	all := make([]SetupOption, 0, len(opts)+1)
	all = append(all, WithInstrumentationName(ClientInstrumentationName))
	all = append(all, opts...)
	return SetupTelemetry(ctx, all...)
}

// SetupTelemetry constructs a core.TracerProvider per the decision
// matrix in the package doc. The returned ShutdownFunc must be
// deferred, so buffered spans flush before the process exits. A
// missing defer commonly causes the "no spans appeared in Grafana"
// surprise.
//
// Errors surface ONLY for unknown exporter names. Endpoint
// construction failures fall back to Noop with a log warning. The
// contract is "a dead observability stack never breaks make demo."
func SetupTelemetry(ctx context.Context, opts ...SetupOption) (core.TracerProvider, ShutdownFunc, error) {
	cfg := setupConfig{stdoutWriter: os.Stdout}
	for _, opt := range opts {
		opt(&cfg)
	}
	applyEnvFallbacks(&cfg)

	switch cfg.exporter {
	case "":
		return core.NoopTracerProvider{}, noopShutdown, nil

	case ExporterStdout:
		exp, err := stdouttrace.New(
			stdouttrace.WithWriter(cfg.stdoutWriter),
			stdouttrace.WithPrettyPrint(),
		)
		if err != nil {
			return nil, nil, fmt.Errorf("SetupTelemetry stdout exporter: %w", err)
		}
		tp, shutdown := buildTracerProvider(exp, &cfg)
		return tp, shutdown, nil

	case ExporterOTLP:
		if err := probeOTLPEndpoint(cfg.otlpEndpoint); err != nil {
			log.Printf("SetupTelemetry: OTLP endpoint %s unreachable (%v) — falling back to Noop. Bring up docker/observability/ to enable tracing, or set EXPORTER='' to silence this warning.", cfg.otlpEndpoint, err)
			return core.NoopTracerProvider{}, noopShutdown, nil
		}
		exp, err := otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(cfg.otlpEndpoint),
			otlptracegrpc.WithInsecure(),
		)
		if err != nil {
			log.Printf("SetupTelemetry: OTLP exporter init failed (%v) — falling back to Noop.", err)
			return core.NoopTracerProvider{}, noopShutdown, nil
		}
		tp, shutdown := buildTracerProvider(exp, &cfg)
		return tp, shutdown, nil

	case ExporterAuto:
		// "auto" is "otlp" with the dial-failure log silenced. The
		// operator explicitly opted into maybe-on-maybe-off
		// semantics, so a missing stack is not surprising and makes
		// no noise. A failed OTLP exporter init still logs. That is
		// separate from dial reachability, e.g. an invalid endpoint
		// format. It is a configuration error, not an environment
		// state.
		if err := probeOTLPEndpoint(cfg.otlpEndpoint); err != nil {
			return core.NoopTracerProvider{}, noopShutdown, nil
		}
		exp, err := otlptracegrpc.New(ctx,
			otlptracegrpc.WithEndpoint(cfg.otlpEndpoint),
			otlptracegrpc.WithInsecure(),
		)
		if err != nil {
			log.Printf("SetupTelemetry (auto): OTLP exporter init failed (%v) — falling back to Noop.", err)
			return core.NoopTracerProvider{}, noopShutdown, nil
		}
		tp, shutdown := buildTracerProvider(exp, &cfg)
		return tp, shutdown, nil

	default:
		return nil, nil, fmt.Errorf("SetupTelemetry: unknown exporter %q (expected %q, %q, %q, or empty)", cfg.exporter, ExporterStdout, ExporterOTLP, ExporterAuto)
	}
}

// buildTracerProvider wraps the supplied exporter in an SDK
// TracerProvider. The provider uses the resolved Resource
// (service.name + extras) and exports synchronously, because demos
// need every span on End(), not after a batch flush window. It
// returns the mcpotel-wrapped core.TracerProvider and a ShutdownFunc.
// The ShutdownFunc flushes and tears down the SDK side.
func buildTracerProvider(exp sdktrace.SpanExporter, cfg *setupConfig) (core.TracerProvider, ShutdownFunc) {
	res := buildResource(cfg)
	sdkTP := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSyncer(exp),
	)
	shutdown := func(ctx context.Context) error {
		return sdkTP.Shutdown(ctx)
	}
	var providerOpts []mcpotel.Option
	if cfg.instrumentationName != "" {
		providerOpts = append(providerOpts, mcpotel.WithInstrumentationName(cfg.instrumentationName))
	}
	return mcpotel.NewProvider(sdkTP, providerOpts...), shutdown
}

// buildResource composes the OTel Resource from service.name +
// explicit WithResourceAttr options + OTEL_RESOURCE_ATTRIBUTES env.
// Explicit options shadow env on key conflict.
func buildResource(cfg *setupConfig) *resource.Resource {
	attrs := make([]attribute.KeyValue, 0, 4+len(cfg.resourceAttr))
	if cfg.serviceName != "" {
		attrs = append(attrs, semconv.ServiceName(cfg.serviceName))
	}
	for k, v := range parseResourceAttrEnv(os.Getenv("OTEL_RESOURCE_ATTRIBUTES")) {
		if _, explicit := cfg.resourceAttr[k]; explicit {
			continue
		}
		attrs = append(attrs, attribute.String(k, v))
	}
	for k, v := range cfg.resourceAttr {
		attrs = append(attrs, attribute.String(k, v))
	}
	return resource.NewSchemaless(attrs...)
}

// applyEnvFallbacks fills in empty config fields from the standard
// OTel env vars before the decision matrix runs. Explicit With...
// options always win. Env is the demo-orchestration default.
func applyEnvFallbacks(cfg *setupConfig) {
	if cfg.serviceName == "" {
		cfg.serviceName = os.Getenv("OTEL_SERVICE_NAME")
	}
	if cfg.otlpEndpoint == "" {
		cfg.otlpEndpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}
	if cfg.otlpEndpoint == "" {
		cfg.otlpEndpoint = DefaultOTLPEndpoint
	}
}

// noopShutdown is the zero-cost ShutdownFunc for the Noop path. It
// is defined once, so the Noop return path doesn't allocate a fresh
// closure per call.
func noopShutdown(context.Context) error { return nil }

// probeOTLPEndpoint TCP-dials the configured endpoint with a short
// timeout to check reachability before constructing the OTLP
// exporter. otlptracegrpc.New is lazy and returns a non-nil exporter
// even when the endpoint is refused. The issue specifies a
// dial-failure fallback (`a dead stack never breaks make demo`), and
// that fallback needs an explicit synchronous check here.
func probeOTLPEndpoint(endpoint string) error {
	conn, err := net.DialTimeout("tcp", endpoint, otlpProbeTimeout)
	if err != nil {
		return err
	}
	_ = conn.Close()
	return nil
}

// UnderlyingOTelTP extracts the OpenTelemetry TracerProvider behind
// a core.TracerProvider from SetupTelemetry. Some downstream
// libraries take an OTel TracerProvider directly, such as oneauth's
// keys.WithTracerProvider or future grpc/http instrumentation. Hand
// them this same OTel TP, so the whole process shares one span
// pipeline.
//
// It returns nil in two cases.
//   - core.NoopTracerProvider{} (the EXPORTER="" path). The caller
//     should treat this as "tracing is off" and skip wiring its
//     own opt-in instrumentation.
//   - Any other custom core.TracerProvider that isn't
//     mcpotel.Provider. These are extension points outside the
//     standard examples wiring.
//
// Defensive callers either nil-check the return or pass it straight
// through (oneauth's options no-op cleanly on nil OTel TPs).
func UnderlyingOTelTP(tp core.TracerProvider) oteltrace.TracerProvider {
	p, ok := tp.(*mcpotel.Provider)
	if !ok {
		return nil
	}
	return p.OTelTracerProvider()
}

// parseResourceAttrEnv parses the OTEL_RESOURCE_ATTRIBUTES env var
// (W3C-style comma-separated key=value pairs) into a map. Malformed
// pairs are silently dropped, since invalid env shouldn't crash a demo.
func parseResourceAttrEnv(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	out := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		eq := strings.IndexByte(pair, '=')
		if eq <= 0 {
			continue
		}
		k := strings.TrimSpace(pair[:eq])
		v := strings.TrimSpace(pair[eq+1:])
		if k == "" {
			continue
		}
		out[k] = v
	}
	return out
}
