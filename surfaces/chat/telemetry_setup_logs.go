package main

// SetupLogs is the logs sibling of SetupTelemetry. It wires the
// otelslog bridge into an OTel LoggerProvider. Then a handler that
// logs via slog.*Context(ctx, ...) ships trace-correlated records to
// the configured collector, and from there to Loki via mcpkit's
// docker/observability/ stack. See telemetry_setup.go for the shared
// decision matrix.
//
// This is the logs half of issue 668. It needs no library seam,
// because stdlib slog is the seam per the modernized issue 7.

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// LoggerInstrumentationName is the default OTel instrumentation
// library label for log records. The otelslog bridge stamps it on
// every emitted record unless the caller overrides it via
// WithInstrumentationName. It matches the trace-side server default
// (mcpotel.defaultInstrumentationName), so backends group
// server-emitted spans and logs under one library.
const LoggerInstrumentationName = "github.com/panyam/mcpkit/server"

// SetupLogs constructs an *slog.Logger per the same decision matrix
// as SetupTelemetry (see telemetry_setup.go). The OTLP-bearing modes
// install the OTel otelslog bridge. Log records then flow through an
// OTel LoggerProvider to the configured collector, and the bridge
// stamps trace correlation on them automatically.
//
// Exporter selector × runtime behavior:
//
//	Exporter==""       → slog.Default() (stdlib). Zero deps pulled
//	                     at runtime, no OTLP side, no SDK lifecycle.
//	                     The returned ShutdownFunc is a no-op so the
//	                     caller's deferred call costs nothing.
//	Exporter=="stdout" → stdoutlog exporter → sdklog.LoggerProvider
//	                     + otelslog bridge. Records pretty-print as
//	                     JSON on the configured writer (default
//	                     os.Stdout). Teaching / demo mode.
//	Exporter=="otlp"   → otlploggrpc exporter → sdklog.LoggerProvider
//	                     + otelslog bridge. Dial-failure logs a
//	                     warning and falls back to slog.Default() so
//	                     a dead docker/observability/ stack never
//	                     breaks `make demo`.
//	Exporter=="auto"   → same as "otlp" but silent on dial-failure
//	                     (the operator explicitly opted into
//	                     maybe-on-maybe-off semantics).
//
// The tracer.Start of mcpkit's ext/otel.Provider installs the active
// span under OTel's own ctx key. The otelslog bridge reads that span
// through OTel's ctx accessor and stamps trace_id + span_id onto
// every record. It does this ONLY when handlers log via
// slog.*Context(ctx, ...). Handlers that call slog.Info(...) without
// ctx lose the pivot. Grafana's Loki datasource renders the
// correlation as a clickable link back to Tempo. mcpkit's
// docker/observability/grafana/ auto-provisions that datasource with
// a `traceID` derived field.
//
// The stdout/otlp/auto modes attach the resolved Resource
// (service.name + WithResourceAttr extras), so Loki labels are
// useful out of the box. The "" mode has no Resource, and the
// caller's slog.Default() does not get its Resource rewritten.
//
// This helper coexists with MCP `notifications/message` logs. It
// covers server-side observability. Client-visible MCP logs go
// through core.MCPLogHandler. The two emit through distinct slog
// handlers and do not interfere. A server can compose both by
// attaching multiple handlers via slogmulti or by emitting through
// two separate loggers.
//
// The shutdown contract mirrors SetupTelemetry. Defer the returned
// ShutdownFunc, so the BatchProcessor flushes buffered records
// before the process exits. Calling shutdown twice is safe.
func SetupLogs(ctx context.Context, opts ...SetupOption) (*slog.Logger, ShutdownFunc, error) {
	cfg := setupConfig{stdoutWriter: os.Stdout}
	for _, opt := range opts {
		opt(&cfg)
	}
	applyEnvFallbacks(&cfg)

	switch cfg.exporter {
	case "":
		return slog.Default(), noopShutdown, nil

	case ExporterStdout:
		exp, err := stdoutlog.New(
			stdoutlog.WithWriter(cfg.stdoutWriter),
			stdoutlog.WithPrettyPrint(),
		)
		if err != nil {
			return nil, nil, fmt.Errorf("SetupLogs stdout exporter: %w", err)
		}
		logger, shutdown := buildLogger(exp, &cfg)
		return logger, shutdown, nil

	case ExporterOTLP:
		if err := probeOTLPEndpoint(cfg.otlpEndpoint); err != nil {
			log.Printf("SetupLogs: OTLP endpoint %s unreachable (%v) — falling back to slog.Default. Bring up docker/observability/ to enable trace-correlated logs, or set EXPORTER='' to silence this warning.", cfg.otlpEndpoint, err)
			return slog.Default(), noopShutdown, nil
		}
		exp, err := otlploggrpc.New(ctx,
			otlploggrpc.WithEndpoint(cfg.otlpEndpoint),
			otlploggrpc.WithInsecure(),
		)
		if err != nil {
			log.Printf("SetupLogs: OTLP exporter init failed (%v) — falling back to slog.Default.", err)
			return slog.Default(), noopShutdown, nil
		}
		logger, shutdown := buildLogger(exp, &cfg)
		return logger, shutdown, nil

	case ExporterAuto:
		// Fall back silently. The operator opted into
		// maybe-on-maybe-off semantics, so a missing stack is not
		// surprising. A failed exporter init still logs. That is
		// separate from dial reachability, e.g. an invalid endpoint
		// format. It is a configuration error, not an environment
		// state.
		if err := probeOTLPEndpoint(cfg.otlpEndpoint); err != nil {
			return slog.Default(), noopShutdown, nil
		}
		exp, err := otlploggrpc.New(ctx,
			otlploggrpc.WithEndpoint(cfg.otlpEndpoint),
			otlploggrpc.WithInsecure(),
		)
		if err != nil {
			log.Printf("SetupLogs (auto): OTLP exporter init failed (%v) — falling back to slog.Default.", err)
			return slog.Default(), noopShutdown, nil
		}
		logger, shutdown := buildLogger(exp, &cfg)
		return logger, shutdown, nil

	default:
		return nil, nil, fmt.Errorf("SetupLogs: unknown exporter %q (expected %q, %q, %q, or empty)", cfg.exporter, ExporterStdout, ExporterOTLP, ExporterAuto)
	}
}

// buildLogger wraps the supplied log exporter in an SDK
// LoggerProvider with the resolved Resource (service.name + extras).
// It installs a BatchProcessor, so records flush on shutdown or at a
// batch boundary. That suits OTLP. The stdout path also batches, so
// demo writes don't race the process exit. It returns an
// otelslog-bridged *slog.Logger and a shutdown closer. The closer
// drains the SDK pipeline.
//
// The instrumentation library name follows the same precedence as
// the trace adapter. An explicit WithInstrumentationName option
// wins, and otherwise it is LoggerInstrumentationName.
func buildLogger(exp sdklog.Exporter, cfg *setupConfig) (*slog.Logger, ShutdownFunc) {
	res := buildResource(cfg)
	lp := sdklog.NewLoggerProvider(
		sdklog.WithResource(res),
		sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
	)
	shutdown := func(ctx context.Context) error {
		return lp.Shutdown(ctx)
	}
	instrumentation := cfg.instrumentationName
	if instrumentation == "" {
		instrumentation = LoggerInstrumentationName
	}
	handler := otelslog.NewHandler(instrumentation, otelslog.WithLoggerProvider(lp))
	return slog.New(handler), shutdown
}

// SetupClientLogs is SetupLogs pre-configured for the host (client)
// side of an example walkthrough. It is equivalent to
// SetupLogs(ctx, opts..., WithInstrumentationName(ClientInstrumentationName)),
// and saves every walkthrough from typing the magic string. An
// explicit WithInstrumentationName(...) in opts still wins, since
// the last option applies.
//
// Pair the returned *slog.Logger with slog.SetDefault(logger) (or
// thread it explicitly) so subsequent slog.*Context(ctx, ...) calls
// on the client side ship to OTLP alongside the host's trace spans.
func SetupClientLogs(ctx context.Context, opts ...SetupOption) (*slog.Logger, ShutdownFunc, error) {
	all := make([]SetupOption, 0, len(opts)+1)
	all = append(all, WithInstrumentationName(ClientInstrumentationName))
	all = append(all, opts...)
	return SetupLogs(ctx, all...)
}
