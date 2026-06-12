package tracing

import (
	"bytes"
	"context"
	"net"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	sc := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    traceID(1),
		SpanID:     spanID(17),
		TraceFlags: oteltrace.FlagsSampled,
		Remote:     true,
	})

	carrier := Encode(sc)
	if len(carrier) != carrierLen {
		t.Fatalf("len(carrier) = %d, want %d", len(carrier), carrierLen)
	}
	got := Decode(carrier)
	if !got.IsValid() {
		t.Fatal("decoded span context is invalid")
	}
	if got.TraceID() != sc.TraceID() {
		t.Fatalf("trace id = %s, want %s", got.TraceID(), sc.TraceID())
	}
	if got.SpanID() != sc.SpanID() {
		t.Fatalf("span id = %s, want %s", got.SpanID(), sc.SpanID())
	}
	if got.TraceFlags() != sc.TraceFlags() {
		t.Fatalf("trace flags = %v, want %v", got.TraceFlags(), sc.TraceFlags())
	}
}

func TestDecodeBadDataInvalid(t *testing.T) {
	for _, tc := range [][]byte{
		nil,
		[]byte("short"),
		make([]byte, carrierLen),
	} {
		if got := Decode(tc); got.IsValid() {
			t.Fatalf("Decode(%v) returned valid span context", tc)
		}
	}
}

func TestInitDisabledReturnsNoopProvider(t *testing.T) {
	provider, err := Init(Config{Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	ctx, span := provider.Tracer("test").Start(context.Background(), "noop")
	defer span.End()
	if span.SpanContext().IsValid() {
		t.Fatalf("noop span context = %s, want invalid", span.SpanContext().TraceID())
	}
	if ctx == nil {
		t.Fatal("ctx is nil")
	}
}

func TestInitExporterFailureDegradesToNoop(t *testing.T) {
	provider, err := Init(Config{
		Enabled:      true,
		OTLPEndpoint: "127.0.0.1:1",
		SampleRatio:  1,
		ServiceName:  "test-service",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, span := provider.Tracer("test").Start(context.Background(), "noop")
	defer span.End()
	if span.SpanContext().IsValid() {
		t.Fatalf("degraded span context = %s, want invalid", span.SpanContext().TraceID())
	}
	if ctx == nil {
		t.Fatal("ctx is nil")
	}
}

func TestInitEnabledWithReachableEndpointCreatesSDKProvider(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			_ = conn.Close()
		}
	}()

	provider, err := Init(Config{
		Enabled:      true,
		OTLPEndpoint: listener.Addr().String(),
		SampleRatio:  1,
		ServiceName:  "test-service",
		Namespace:    "tsu",
		Environment:  "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, span := provider.Tracer("test").Start(context.Background(), "sampled")
	if !span.SpanContext().IsValid() {
		t.Fatal("enabled provider span context is invalid")
	}
	span.End()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = provider.Shutdown(ctx)
}

func TestSamplerParentBased(t *testing.T) {
	sampler := Sampler(0)
	parent := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    traceID(3),
		SpanID:     spanID(4),
		TraceFlags: oteltrace.FlagsSampled,
		Remote:     true,
	})
	ctx := oteltrace.ContextWithRemoteSpanContext(context.Background(), parent)
	result := sampler.ShouldSample(sdktrace.SamplingParameters{
		ParentContext: ctx,
		TraceID:       traceID(5),
		Name:          "child",
	})
	if result.Decision != sdktrace.RecordAndSample {
		t.Fatalf("sampled parent decision = %v, want RecordAndSample", result.Decision)
	}

	root := Sampler(1).ShouldSample(sdktrace.SamplingParameters{TraceID: traceID(6), Name: "root"})
	if root.Decision != sdktrace.RecordAndSample {
		t.Fatalf("ratio=1 root decision = %v, want RecordAndSample", root.Decision)
	}

	dropped := Sampler(0).ShouldSample(sdktrace.SamplingParameters{TraceID: traceID(7), Name: "root"})
	if dropped.Decision != sdktrace.Drop {
		t.Fatalf("ratio=0 root decision = %v, want Drop", dropped.Decision)
	}

	tapped := Sampler(0).ShouldSample(sdktrace.SamplingParameters{
		ParentContext: WithReplayCapture(context.Background()),
		TraceID:       traceID(8),
		Name:          "tap",
	})
	if tapped.Decision != sdktrace.RecordAndSample {
		t.Fatalf("tap decision = %v, want RecordAndSample", tapped.Decision)
	}
}

func TestProviderNilFallbacks(t *testing.T) {
	if err := (*Provider)(nil).Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, span := (*Provider)(nil).Tracer("test").Start(context.Background(), "noop")
	defer span.End()
	if span.SpanContext().IsValid() {
		t.Fatal("nil provider tracer produced valid span")
	}
}

func TestNormalizeRatioBounds(t *testing.T) {
	for _, tt := range []struct {
		in   float64
		want float64
	}{
		{in: -1, want: 0},
		{in: 0.5, want: 0.5},
		{in: 2, want: 1},
	} {
		if got := normalizeRatio(tt.in); got != tt.want {
			t.Fatalf("normalizeRatio(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestHTTPMiddlewareHandlesAndSkipsRoutes(t *testing.T) {
	app := fiber.New()
	app.Use(HTTPMiddleware("/skip"))
	app.Get("/ok/:id", func(ctx fiber.Ctx) error {
		return ctx.SendString("ok")
	})
	app.Get("/skip", func(ctx fiber.Ctx) error {
		return ctx.SendString("skip")
	})

	for _, path := range []string{"/ok/42", "/skip"} {
		res, err := app.Test(httptest.NewRequest("GET", path, nil))
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != 200 {
			t.Fatalf("%s status = %d, want 200", path, res.StatusCode)
		}
	}
}

func TestBodyCaptureMiddlewareRecordsRedactedJSONBodies(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(recorder),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(oteltrace.NewNoopTracerProvider())
	})

	app := fiber.New()
	app.Use(HTTPMiddleware(), BodyCaptureMiddleware(ReplayConfig{
		BodyMaxBytes:   1024,
		FieldBlacklist: []string{"password", "token"},
	}))
	app.Post("/login", func(ctx fiber.Ctx) error {
		return ctx.JSON(map[string]any{"ok": true, "session_token": "response-secret"})
	})

	req := httptest.NewRequest("POST", "/login", bytes.NewBufferString(`{"username":"alice","password":"request-secret"}`))
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	res, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	attrs := spanAttrs(spans[0].Attributes())
	if got := attrs[AttrHTTPRequestBody]; got != `{"password":"***","username":"alice"}` {
		t.Fatalf("request body attr = %q", got)
	}
	if got := attrs[AttrHTTPResponseBody]; got != `{"ok":true,"session_token":"***"}` {
		t.Fatalf("response body attr = %q", got)
	}
	if attrs[AttrHTTPRequestBodyTruncated] != "false" || attrs[AttrHTTPResponseBodyTruncated] != "false" {
		t.Fatalf("unexpected truncation attrs: %#v", attrs)
	}
}

func TestBodyCaptureMiddlewareSkipsAndTruncates(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(recorder),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(oteltrace.NewNoopTracerProvider())
	})

	app := fiber.New()
	app.Use(HTTPMiddleware(), BodyCaptureMiddleware(ReplayConfig{
		BodyMaxBytes:    12,
		FieldBlacklist:  []string{"password"},
		SensitiveRoutes: []string{"/private*"},
	}))
	app.Post("/private/echo", func(ctx fiber.Ctx) error {
		return ctx.JSON(map[string]string{"status": "hidden"})
	})
	app.Post("/echo", func(ctx fiber.Ctx) error {
		return ctx.JSON(map[string]string{"status": "abcdefghijklmnopqrstuvwxyz"})
	})

	sensitiveReq := httptest.NewRequest("POST", "/private/echo", bytes.NewBufferString(`{"password":"secret"}`))
	sensitiveReq.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	res, err := app.Test(sensitiveReq)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("sensitive request status=%d, want 200", res.StatusCode)
	}

	req := httptest.NewRequest("POST", "/echo", bytes.NewBufferString(`{"value":"abcdefghijklmnopqrstuvwxyz"}`))
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	res, err = app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}

	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("ended spans = %d, want 2", len(spans))
	}
	sensitiveAttrs := spanAttrs(spans[0].Attributes())
	if _, ok := sensitiveAttrs[AttrHTTPRequestBody]; ok {
		t.Fatalf("sensitive route recorded request body: %#v", sensitiveAttrs)
	}
	if sensitiveAttrs[AttrHTTPBodySkipped] != "sensitive_route" {
		t.Fatalf("sensitive route skipped attr = %#v", sensitiveAttrs[AttrHTTPBodySkipped])
	}
	echoAttrs := spanAttrs(spans[1].Attributes())
	if echoAttrs[AttrHTTPRequestBodyTruncated] != "true" || echoAttrs[AttrHTTPResponseBodyTruncated] != "true" {
		t.Fatalf("expected truncation attrs: %#v", echoAttrs)
	}
	if len(echoAttrs[AttrHTTPRequestBody]) != 12 || len(echoAttrs[AttrHTTPResponseBody]) != 12 {
		t.Fatalf("unexpected truncated body lengths: request=%q response=%q", echoAttrs[AttrHTTPRequestBody], echoAttrs[AttrHTTPResponseBody])
	}
}

func TestTapMiddlewareAuthorizesDevAndProdToken(t *testing.T) {
	app := fiber.New()
	app.Use(TapMiddleware(ReplayConfig{Environment: "dev"}))
	app.Get("/tap", func(ctx fiber.Ctx) error {
		if !IsReplayCapture(ctx.Context()) {
			t.Fatal("dev tap was not authorized")
		}
		return ctx.SendStatus(fiber.StatusNoContent)
	})
	req := httptest.NewRequest("GET", "/tap", nil)
	req.Header.Set("X-Tsu-Capture", "1")
	res, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != fiber.StatusNoContent {
		t.Fatalf("dev tap status=%d, want %d", res.StatusCode, fiber.StatusNoContent)
	}

	prod := fiber.New()
	prod.Use(TapMiddleware(ReplayConfig{Environment: "prod", CaptureToken: "secret"}))
	prod.Get("/tap", func(ctx fiber.Ctx) error {
		if !IsReplayCapture(ctx.Context()) {
			t.Fatal("prod token tap was not authorized")
		}
		return ctx.SendStatus(fiber.StatusNoContent)
	})
	req = httptest.NewRequest("GET", "/tap", nil)
	req.Header.Set("X-Tsu-Capture", "secret")
	res, err = prod.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != fiber.StatusNoContent {
		t.Fatalf("prod tap status=%d, want %d", res.StatusCode, fiber.StatusNoContent)
	}
}

func traceID(seed byte) oteltrace.TraceID {
	var id oteltrace.TraceID
	for i := range id {
		id[i] = seed + byte(i)
	}
	return id
}

func spanAttrs(attrs []attribute.KeyValue) map[string]string {
	out := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		out[string(attr.Key)] = attr.Value.Emit()
	}
	return out
}

func spanID(seed byte) oteltrace.SpanID {
	var id oteltrace.SpanID
	for i := range id {
		id[i] = seed + byte(i)
	}
	return id
}
