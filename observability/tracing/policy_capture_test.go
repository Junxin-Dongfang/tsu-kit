package tracing

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type captureAllowedKey struct{}

func TestPolicyBodyCaptureFailsClosedWithoutDecision(t *testing.T) {
	attrs := runPolicyCaptureRequest(t, nil, "/echo", `{"password":"secret"}`)
	assertPolicyCaptureAttrsAbsent(t, attrs)
}

func TestPolicyBodyCaptureFailsClosedWithEmptyDecision(t *testing.T) {
	policy := BodyCapturePolicyFunc(func(fiber.Ctx) BodyCaptureDecision {
		return BodyCaptureDecision{}
	})
	attrs := runPolicyCaptureRequest(t, policy, "/echo", `{"password":"secret"}`)
	assertPolicyCaptureAttrsAbsent(t, attrs)
}

func TestPolicyBodyCaptureDecidesAfterHandlerAndCapturesSelectedSide(t *testing.T) {
	policy := BodyCapturePolicyFunc(func(ctx fiber.Ctx) BodyCaptureDecision {
		allowed, _ := ctx.Context().Value(captureAllowedKey{}).(bool)
		return BodyCaptureDecision{TaskID: "41", RequestBody: allowed}
	})
	attrs := runPolicyCaptureRequest(t, policy, "/echo", `{"password":"secret","value":"ok"}`)
	if attrs[AttrHTTPRequestBody] != `{"password":"***","value":"ok"}` {
		t.Fatalf("request body = %q", attrs[AttrHTTPRequestBody])
	}
	if _, ok := attrs[AttrHTTPResponseBody]; ok {
		t.Fatalf("response body captured when side disabled: %#v", attrs)
	}
	if attrs[AttrHTTPBodySanitized] != "true" || attrs[AttrHTTPBodyCaptureTaskID] != "41" {
		t.Fatalf("capture provenance missing: %#v", attrs)
	}
}

func TestPolicyBodyCaptureSensitiveRouteAlwaysWins(t *testing.T) {
	policy := BodyCapturePolicyFunc(func(fiber.Ctx) BodyCaptureDecision {
		return BodyCaptureDecision{TaskID: "42", RequestBody: true, ResponseBody: true}
	})
	attrs := runPolicyCaptureRequest(t, policy, "/private/login", `{"password":"secret"}`)
	if _, ok := attrs[AttrHTTPRequestBody]; ok {
		t.Fatalf("sensitive request body captured: %#v", attrs)
	}
	if _, ok := attrs[AttrHTTPResponseBody]; ok {
		t.Fatalf("sensitive response body captured: %#v", attrs)
	}
	if _, ok := attrs[AttrHTTPBodySanitized]; ok {
		t.Fatalf("sensitive route recorded sanitized provenance: %#v", attrs)
	}
	if _, ok := attrs[AttrHTTPBodyCaptureTaskID]; ok {
		t.Fatalf("sensitive route recorded task provenance: %#v", attrs)
	}
	if attrs[AttrHTTPBodySkipped] != "sensitive_route" {
		t.Fatalf("skip reason = %q", attrs[AttrHTTPBodySkipped])
	}
}

func TestPolicyBodyCaptureCapturesResponseOnly(t *testing.T) {
	policy := BodyCapturePolicyFunc(func(fiber.Ctx) BodyCaptureDecision {
		return BodyCaptureDecision{TaskID: "44", ResponseBody: true}
	})
	attrs := runPolicyCaptureRequest(t, policy, "/echo", `{"password":"request-secret"}`)
	if _, ok := attrs[AttrHTTPRequestBody]; ok {
		t.Fatalf("request body captured for response-only decision: %#v", attrs)
	}
	if got := attrs[AttrHTTPResponseBody]; got != `{"ok":true,"token":"***"}` {
		t.Fatalf("response body = %q", got)
	}
	if attrs[AttrHTTPBodySanitized] != "true" || attrs[AttrHTTPBodyCaptureTaskID] != "44" {
		t.Fatalf("capture provenance missing: %#v", attrs)
	}
}

func TestPolicyBodyCaptureRejectsNonJSONAndTruncates(t *testing.T) {
	for _, tc := range []struct {
		name         string
		contentType  string
		body         string
		maxBytes     int
		response     bool
		wantRequest  bool
		wantResponse bool
		wantCut      bool
	}{
		{name: "text plain", contentType: fiber.MIMETextPlain, body: `{"value":"ok"}`, maxBytes: 1024},
		{name: "invalid json", contentType: fiber.MIMEApplicationJSON, body: `{invalid`, maxBytes: 1024},
		{name: "truncated", contentType: fiber.MIMEApplicationJSON, body: `{"value":"abcdefghijklmnopqrstuvwxyz"}`, maxBytes: 12, response: true, wantRequest: true, wantResponse: true, wantCut: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := BodyCapturePolicyFunc(func(fiber.Ctx) BodyCaptureDecision {
				return BodyCaptureDecision{TaskID: "43", RequestBody: true, ResponseBody: tc.response}
			})
			attrs := runPolicyCaptureRequestWithConfig(t, policy, "/echo", tc.body, tc.contentType, ReplayConfig{
				BodyMaxBytes:   tc.maxBytes,
				FieldBlacklist: []string{"password", "token"},
			})
			_, requestCaptured := attrs[AttrHTTPRequestBody]
			_, responseCaptured := attrs[AttrHTTPResponseBody]
			if requestCaptured != tc.wantRequest || responseCaptured != tc.wantResponse {
				t.Fatalf("captured request=%t response=%t attrs=%#v", requestCaptured, responseCaptured, attrs)
			}
			if tc.wantCut && (attrs[AttrHTTPRequestBodyTruncated] != "true" || attrs[AttrHTTPResponseBodyTruncated] != "true") {
				t.Fatalf("truncation attrs=%#v", attrs)
			}
		})
	}
}

func TestPolicyBodyCaptureRejectsNonJSONAndInvalidJSONResponses(t *testing.T) {
	policy := BodyCapturePolicyFunc(func(fiber.Ctx) BodyCaptureDecision {
		return BodyCaptureDecision{TaskID: "45", ResponseBody: true}
	})
	for _, tc := range []struct {
		name                string
		responseBody        string
		responseContentType string
	}{
		{name: "text plain", responseBody: `{"token":"response-secret"}`, responseContentType: fiber.MIMETextPlain},
		{name: "invalid json", responseBody: `{invalid`, responseContentType: fiber.MIMEApplicationJSON},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attrs := runPolicyCaptureRequestWithResponse(t, policy, "/echo", `{"value":"ok"}`, fiber.MIMEApplicationJSON, ReplayConfig{
				BodyMaxBytes:   1024,
				FieldBlacklist: []string{"password", "token"},
			}, tc.responseBody, tc.responseContentType)
			assertPolicyCaptureAttrsAbsent(t, attrs)
		})
	}
}

func assertPolicyCaptureAttrsAbsent(t *testing.T, attrs map[string]string) {
	t.Helper()
	for _, key := range []string{
		AttrHTTPRequestBody,
		AttrHTTPResponseBody,
		AttrHTTPBodySanitized,
		AttrHTTPBodyCaptureTaskID,
	} {
		if _, ok := attrs[key]; ok {
			t.Fatalf("unexpected %s: %#v", key, attrs)
		}
	}
}

func runPolicyCaptureRequest(t *testing.T, policy BodyCapturePolicy, path, body string) map[string]string {
	t.Helper()
	return runPolicyCaptureRequestWithConfig(t, policy, path, body, fiber.MIMEApplicationJSON, ReplayConfig{
		BodyMaxBytes:    1024,
		FieldBlacklist:  []string{"password", "token"},
		SensitiveRoutes: []string{"/private*"},
	})
}

func runPolicyCaptureRequestWithConfig(t *testing.T, policy BodyCapturePolicy, path, body, contentType string, cfg ReplayConfig) map[string]string {
	t.Helper()
	return runPolicyCaptureRequestWithResponse(t, policy, path, body, contentType, cfg, `{"ok":true,"token":"response-secret"}`, fiber.MIMEApplicationJSON)
}

func runPolicyCaptureRequestWithResponse(t *testing.T, policy BodyCapturePolicy, path, body, contentType string, cfg ReplayConfig, responseBody, responseContentType string) map[string]string {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(recorder),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(oteltrace.NewNoopTracerProvider()) })

	app := fiber.New()
	app.Use(HTTPMiddleware(), PolicyBodyCaptureMiddleware(cfg, policy))
	app.Post(path, func(ctx fiber.Ctx) error {
		ctx.SetContext(context.WithValue(ctx.Context(), captureAllowedKey{}, true))
		ctx.Set(fiber.HeaderContentType, responseContentType)
		return ctx.SendString(responseBody)
	})
	req := httptest.NewRequest("POST", path, bytes.NewBufferString(body))
	req.Header.Set(fiber.HeaderContentType, contentType)
	res, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatalf("request status=%d want=200", res.StatusCode)
	}
	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans=%d want=1", len(spans))
	}
	return spanAttrs(spans[0].Attributes())
}
