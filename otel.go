package main

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/contrib/exporters/autoexport"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.27.0"
)

var res = resource.NewWithAttributes(
	semconv.SchemaURL,
	semconv.ServiceNameKey.String("otlp-metrics-processor-backend"),
	semconv.ServiceNamespaceKey.String("dash0-exercise"),
	semconv.ServiceVersionKey.String("1.0.0"),
)

// setupOTelSDK bootstraps the OpenTelemetry pipeline.
// If it does not return an error, make sure to call shutdown for proper cleanup.
func setupOTelSDK(ctx context.Context) (shutdown func(context.Context) error, err error) {
	var shutdownFuncs []func(context.Context) error

	// shutdown calls cleanup functions registered via shutdownFuncs.
	// The errors from the calls are joined.
	// Each registered cleanup will be invoked once.
	shutdown = func(ctx context.Context) error {
		var err error
		for _, fn := range shutdownFuncs {
			err = errors.Join(err, fn(ctx))
		}
		shutdownFuncs = nil
		return err
	}

	// handleErr calls shutdown for cleanup and makes sure that all errors are returned.
	handleErr := func(inErr error) {
		err = errors.Join(inErr, shutdown(ctx))
	}

	// Set up propagator.
	prop := newPropagator()
	otel.SetTextMapPropagator(prop)

	// Set up trace provider.
	tracerProvider, err := newTraceProvider(ctx)
	if err != nil {
		handleErr(err)
		return
	}
	shutdownFuncs = append(shutdownFuncs, tracerProvider.Shutdown)
	otel.SetTracerProvider(tracerProvider)

	// Set up meter provider.
	meterProvider, err := newMeterProvider(ctx)
	if err != nil {
		handleErr(err)
		return
	}
	shutdownFuncs = append(shutdownFuncs, meterProvider.Shutdown)
	otel.SetMeterProvider(meterProvider)

	// Set up logger provider.
	loggerProvider, err := newLoggerProvider(ctx)
	if err != nil {
		handleErr(err)
		return
	}
	shutdownFuncs = append(shutdownFuncs, loggerProvider.Shutdown)
	global.SetLoggerProvider(loggerProvider)

	return
}

func newPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}

// newTraceProvider exports through the exporter selected by the standard
// OTEL_TRACES_EXPORTER variable (e.g. otlp, none), and to stdout when it is
// unset. The sampler is read from OTEL_TRACES_SAMPLER (default: always on).
func newTraceProvider(ctx context.Context) (*trace.TracerProvider, error) {
	traceExporter, err := autoexport.NewSpanExporter(ctx, autoexport.WithFallbackSpanExporter(
		func(context.Context) (trace.SpanExporter, error) {
			return stdouttrace.New(stdouttrace.WithPrettyPrint())
		}))
	if err != nil {
		return nil, err
	}

	traceProvider := trace.NewTracerProvider(
		trace.WithResource(res),
		trace.WithBatcher(traceExporter,
			// Default is 5s. Set to 1s for demonstrative purposes.
			trace.WithBatchTimeout(time.Second)),
	)
	return traceProvider, nil
}

// newMeterProvider exports through the exporter selected by the standard
// OTEL_METRICS_EXPORTER variable (e.g. otlp), and to stdout when it is unset.
func newMeterProvider(ctx context.Context) (*metric.MeterProvider, error) {
	metricReader, err := autoexport.NewMetricReader(ctx, autoexport.WithFallbackMetricReader(
		func(context.Context) (metric.Reader, error) {
			metricExporter, err := stdoutmetric.New(stdoutmetric.WithPrettyPrint())
			if err != nil {
				return nil, err
			}
			// Default is 1m. Set to 10s for demonstrative purposes.
			return metric.NewPeriodicReader(metricExporter, metric.WithInterval(10*time.Second)), nil
		}))
	if err != nil {
		return nil, err
	}

	meterProvider := metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(metricReader),
	)
	return meterProvider, nil
}

// newLoggerProvider exports through the exporter selected by the standard
// OTEL_LOGS_EXPORTER variable (e.g. otlp), and to stdout when it is unset.
func newLoggerProvider(ctx context.Context) (*log.LoggerProvider, error) {
	logExporter, err := autoexport.NewLogExporter(ctx, autoexport.WithFallbackLogExporter(
		func(context.Context) (log.Exporter, error) {
			return stdoutlog.New(stdoutlog.WithPrettyPrint())
		}))
	if err != nil {
		return nil, err
	}

	loggerProvider := log.NewLoggerProvider(
		log.WithResource(res),
		log.WithProcessor(log.NewBatchProcessor(logExporter)),
	)
	return loggerProvider, nil
}
