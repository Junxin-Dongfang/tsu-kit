package tracing

import (
	"context"
	"log"
	"math"
	"net"
	"strings"
	"time"

	fiberotel "github.com/gofiber/contrib/v3/otel"
	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const (
	defaultNamespace    = "tsu"
	defaultServiceName  = "unknown"
	defaultOTLPEndpoint = "tempo:4317"
	carrierLen          = 25
)

// Config identifies one process-local OpenTelemetry tracer provider.
type Config struct {
	Enabled        bool
	OTLPEndpoint   string
	SampleRatio    float64
	ServiceName    string
	Namespace      string
	Environment    string
	ServiceVersion string
}

// Provider owns a process-local tracer provider.
type Provider struct {
	provider oteltrace.TracerProvider
	shutdown func(context.Context) error
}

// Init creates a tracer provider backed by OTLP/gRPC. Exporter setup failures
// degrade to a noop provider so tracing never prevents a service from starting.
func Init(cfg Config) (*Provider, error) {
	cfg = normalizeConfig(cfg)
	if !cfg.Enabled {
		return noopProvider(), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	res, err := resource.New(ctx,
		resource.WithAttributes(
			attribute.String("service.name", cfg.ServiceName),
			attribute.String("service.namespace", cfg.Namespace),
			attribute.String("deployment.environment", cfg.Environment),
			attribute.String("service.version", cfg.ServiceVersion),
		),
	)
	if err != nil {
		log.Printf("tracing: initialize resource failed, using noop provider: %v", err)
		return noopProvider(), nil
	}

	if err := checkOTLPEndpoint(ctx, cfg.OTLPEndpoint); err != nil {
		log.Printf("tracing: OTLP endpoint unavailable, using noop provider: %v", err)
		return noopProvider(), nil
	}

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(cfg.OTLPEndpoint),
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithTimeout(5*time.Second),
	)
	if err != nil {
		log.Printf("tracing: initialize OTLP exporter failed, using noop provider: %v", err)
		return noopProvider(), nil
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(Sampler(cfg.SampleRatio)),
		sdktrace.WithBatcher(exporter),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	return &Provider{
		provider: tp,
		shutdown: func(ctx context.Context) error {
			return tp.Shutdown(ctx)
		},
	}, nil
}

// Shutdown flushes queued spans.
func (p *Provider) Shutdown(ctx context.Context) error {
	if p == nil || p.shutdown == nil {
		return nil
	}
	return p.shutdown(ctx)
}

// Tracer returns an OpenTelemetry tracer from this provider.
func (p *Provider) Tracer(name string, opts ...oteltrace.TracerOption) oteltrace.Tracer {
	if p == nil || p.provider == nil {
		return oteltrace.NewNoopTracerProvider().Tracer(name, opts...)
	}
	return p.provider.Tracer(name, opts...)
}

// Sampler returns the head sampler used by HTTP/gate entry points and node.
func Sampler(ratio float64) sdktrace.Sampler {
	return replaySampler{delegate: sdktrace.ParentBased(sdktrace.TraceIDRatioBased(normalizeRatio(ratio)))}
}

type replaySampler struct {
	delegate sdktrace.Sampler
}

func (s replaySampler) ShouldSample(params sdktrace.SamplingParameters) sdktrace.SamplingResult {
	if IsReplayCapture(params.ParentContext) {
		return sdktrace.SamplingResult{
			Decision:   sdktrace.RecordAndSample,
			Tracestate: oteltrace.SpanContextFromContext(params.ParentContext).TraceState(),
		}
	}
	return s.delegate.ShouldSample(params)
}

func (s replaySampler) Description() string {
	return "ReplayCapture{" + s.delegate.Description() + "}"
}

// Encode converts an OpenTelemetry SpanContext into a neutral 25-byte carrier.
func Encode(sc oteltrace.SpanContext) []byte {
	if !sc.IsValid() {
		return nil
	}
	tc := make([]byte, carrierLen)
	traceID := sc.TraceID()
	spanID := sc.SpanID()
	copy(tc[:16], traceID[:])
	copy(tc[16:24], spanID[:])
	tc[24] = byte(sc.TraceFlags())
	return tc
}

// Decode converts a neutral 25-byte carrier into an OpenTelemetry SpanContext.
func Decode(tc []byte) oteltrace.SpanContext {
	if len(tc) != carrierLen {
		return oteltrace.SpanContext{}
	}
	var traceID oteltrace.TraceID
	var spanID oteltrace.SpanID
	copy(traceID[:], tc[:16])
	copy(spanID[:], tc[16:24])
	if !traceID.IsValid() || !spanID.IsValid() {
		return oteltrace.SpanContext{}
	}
	return oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: oteltrace.TraceFlags(tc[24]),
		Remote:     true,
	})
}

// HTTPMiddleware traces Fiber v3 requests through due's component HTTP server.
func HTTPMiddleware(skipPrefixes ...string) fiber.Handler {
	return fiberotel.Middleware(
		fiberotel.WithoutMetrics(true),
		fiberotel.WithNext(func(ctx fiber.Ctx) bool {
			path := ctx.Path()
			for _, prefix := range skipPrefixes {
				if prefix != "" && strings.HasPrefix(path, prefix) {
					return true
				}
			}
			return false
		}),
		fiberotel.WithSpanNameFormatter(func(ctx fiber.Ctx) string {
			route := ctx.Path()
			if ctx.Route() != nil && ctx.Route().Path != "" {
				route = ctx.Route().Path
			}
			return "HTTP " + ctx.Method() + " " + route
		}),
	)
}

func noopProvider() *Provider {
	return &Provider{provider: oteltrace.NewNoopTracerProvider()}
}

// NewProvider wraps an already-constructed TracerProvider with an optional
// shutdown function. This is intended for testing and advanced embedding;
// production code should use Init.
func NewProvider(tp oteltrace.TracerProvider, shutdown func(context.Context) error) *Provider {
	return &Provider{provider: tp, shutdown: shutdown}
}

func normalizeConfig(cfg Config) Config {
	cfg.OTLPEndpoint = strings.TrimSpace(cfg.OTLPEndpoint)
	if cfg.OTLPEndpoint == "" {
		cfg.OTLPEndpoint = defaultOTLPEndpoint
	}
	cfg.ServiceName = strings.TrimSpace(cfg.ServiceName)
	if cfg.ServiceName == "" {
		cfg.ServiceName = defaultServiceName
	}
	cfg.Namespace = strings.TrimSpace(cfg.Namespace)
	if cfg.Namespace == "" {
		cfg.Namespace = defaultNamespace
	}
	cfg.Environment = strings.TrimSpace(cfg.Environment)
	if cfg.Environment == "" {
		cfg.Environment = "unknown"
	}
	cfg.ServiceVersion = strings.TrimSpace(cfg.ServiceVersion)
	if cfg.ServiceVersion == "" {
		cfg.ServiceVersion = "unknown"
	}
	cfg.SampleRatio = normalizeRatio(cfg.SampleRatio)
	return cfg
}

func normalizeRatio(ratio float64) float64 {
	if math.IsNaN(ratio) || ratio < 0 {
		return 0
	}
	if ratio > 1 {
		return 1
	}
	return ratio
}

func checkOTLPEndpoint(ctx context.Context, endpoint string) error {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", endpoint)
	if err != nil {
		return err
	}
	return conn.Close()
}
