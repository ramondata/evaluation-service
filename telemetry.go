package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// A ausencia do endpoint mantem o desenvolvimento local sem Collector.
func initTelemetry(name string) (func(), error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func() {}, nil
	}
	c := context.Background()
	res := resource.NewSchemaless(attribute.String("service.name", name),
		attribute.String("service.namespace", "toggle-master"),
		attribute.String("service.instance.id", os.Getenv("HOSTNAME")))
	te, err := otlptracehttp.New(c)
	if err != nil {
		return nil, err
	}
	me, err := otlpmetrichttp.New(c)
	if err != nil {
		return nil, err
	}
	le, err := otlploghttp.New(c)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithBatcher(te))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(me, sdkmetric.WithInterval(10*time.Second))))
	lp := sdklog.NewLoggerProvider(sdklog.WithResource(res), sdklog.WithProcessor(sdklog.NewBatchProcessor(le)))
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	// Logs legados continuam no stdout; logs HTTP correlacionados seguem via OTLP.
	slog.SetDefault(slog.New(otelslog.NewHandler(name, otelslog.WithLoggerProvider(lp))))
	return func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := errors.Join(tp.Shutdown(c), mp.Shutdown(c), lp.Shutdown(c)); err != nil {
			log.Printf("telemetry shutdown: %v", err)
		}
	}, nil
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) { w.status = code; w.ResponseWriter.WriteHeader(code) }

func observeHTTP(next http.Handler) http.Handler {
	counter, err := otel.Meter("togglemaster").Int64Counter("toggle_http_requests")
	if err != nil {
		panic(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		counter.Add(r.Context(), 1, metric.WithAttributes(
			attribute.String("http_method", r.Method), attribute.Int("status_code", sw.status)))
		slog.InfoContext(r.Context(), "http_request", "method", r.Method, "status", sw.status)
	})
	return otelhttp.NewHandler(handler, "http.server", otelhttp.WithFilter(func(r *http.Request) bool {
		return r.URL.Path != "/health"
	}), otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string { return r.Method + " " + routeName(r.URL.Path) }))
}

func routeName(path string) string {
	switch path {
	case "/evaluate", "/validate", "/admin/keys", "/demo/failure":
		return path
	}
	return "other"
}
