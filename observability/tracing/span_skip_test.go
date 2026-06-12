package tracing

import (
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// TestHTTPMiddlewareSpanCreationAndSkip verifies that HTTPMiddleware creates a
// server span for tracked routes and produces no span for paths that match a
// configured skip prefix.
func TestHTTPMiddlewareSpanCreationAndSkip(t *testing.T) {
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
	app.Use(HTTPMiddleware("/skip"))
	app.Get("/ok", func(ctx fiber.Ctx) error {
		return ctx.SendString("ok")
	})
	app.Get("/skip/health", func(ctx fiber.Ctx) error {
		return ctx.SendString("skip")
	})

	t.Run("tracked route creates one server span", func(t *testing.T) {
		before := len(recorder.Ended())

		res, err := app.Test(httptest.NewRequest("GET", "/ok", nil))
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != 200 {
			t.Fatalf("status = %d, want 200", res.StatusCode)
		}

		after := recorder.Ended()
		if len(after)-before != 1 {
			t.Fatalf("new spans = %d, want 1 (total=%d)", len(after)-before, len(after))
		}

		// The rule promises a *server* span, not merely some span: assert SpanKind.
		span := after[len(after)-1]
		if kind := span.SpanKind(); kind != oteltrace.SpanKindServer {
			t.Fatalf("span kind = %v, want %v (server)", kind, oteltrace.SpanKindServer)
		}
	})

	t.Run("skip-prefix route produces no span", func(t *testing.T) {
		before := len(recorder.Ended())

		res, err := app.Test(httptest.NewRequest("GET", "/skip/health", nil))
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != 200 {
			t.Fatalf("status = %d, want 200", res.StatusCode)
		}

		after := recorder.Ended()
		if len(after) != before {
			t.Fatalf("new spans = %d, want 0 (total=%d)", len(after)-before, len(after))
		}
	})
}
