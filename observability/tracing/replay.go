package tracing

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"mime"
	"strings"

	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const (
	AttrHTTPRequestBody           = "tsu.http.request_body"
	AttrHTTPResponseBody          = "tsu.http.response_body"
	AttrHTTPRequestBodyTruncated  = "tsu.http.request_body_truncated"
	AttrHTTPResponseBodyTruncated = "tsu.http.response_body_truncated"
	AttrHTTPBodySkipped           = "tsu.http.body_skipped"
)

type replayCaptureKey struct{}

// ReplayConfig controls request replay capture for HTTP spans.
type ReplayConfig struct {
	BodyMaxBytes     int
	FieldBlacklist   []string
	SensitiveRoutes  []string
	SQLParameterMode string
	CaptureHeader    string
	CaptureToken     string
	Environment      string
}

// TapMiddleware marks authorized per-request capture before HTTPMiddleware
// starts the server span, allowing the sampler to force RecordAndSample.
func TapMiddleware(cfg ReplayConfig) fiber.Handler {
	cfg = normalizeReplayConfig(cfg)
	return func(ctx fiber.Ctx) error {
		if isAuthorizedTap(ctx, cfg) {
			ctx.SetContext(WithReplayCapture(ctx.Context()))
		}
		return ctx.Next()
	}
}

// BodyCaptureMiddleware records JSON request and response bodies on the active
// HTTP span. Capture failures degrade by omitting attributes.
func BodyCaptureMiddleware(cfg ReplayConfig) fiber.Handler {
	cfg = normalizeReplayConfig(cfg)
	return func(ctx fiber.Ctx) error {
		span := oteltrace.SpanFromContext(ctx.Context())
		if span.IsRecording() && !matchesRoute(ctx.Path(), cfg.SensitiveRoutes) {
			if value, truncated, ok := captureBody(ctx.Body(), ctx.Get(fiber.HeaderContentType), cfg, IsReplayCapture(ctx.Context())); ok {
				span.SetAttributes(attribute.String(AttrHTTPRequestBody, value), attribute.Bool(AttrHTTPRequestBodyTruncated, truncated))
			}
		} else if span.IsRecording() {
			span.SetAttributes(attribute.String(AttrHTTPBodySkipped, "sensitive_route"))
		}

		err := ctx.Next()

		span = oteltrace.SpanFromContext(ctx.Context())
		if span.IsRecording() && !matchesRoute(ctx.Path(), cfg.SensitiveRoutes) {
			if value, truncated, ok := captureBody(ctx.Response().Body(), ctx.GetRespHeader(fiber.HeaderContentType), cfg, IsReplayCapture(ctx.Context())); ok {
				span.SetAttributes(attribute.String(AttrHTTPResponseBody, value), attribute.Bool(AttrHTTPResponseBodyTruncated, truncated))
			}
		}
		return err
	}
}

func WithReplayCapture(ctx context.Context) context.Context {
	return context.WithValue(ctx, replayCaptureKey{}, true)
}

func IsReplayCapture(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(replayCaptureKey{}).(bool)
	return v
}

func normalizeReplayConfig(cfg ReplayConfig) ReplayConfig {
	if cfg.BodyMaxBytes <= 0 {
		cfg.BodyMaxBytes = 4 * 1024
	}
	if strings.TrimSpace(cfg.CaptureHeader) == "" {
		cfg.CaptureHeader = "X-Tsu-Capture"
	}
	if len(cfg.FieldBlacklist) == 0 {
		cfg.FieldBlacklist = []string{"password", "token", "secret", "authorization", "cookie", "session"}
	}
	return cfg
}

func isAuthorizedTap(ctx fiber.Ctx, cfg ReplayConfig) bool {
	value := strings.TrimSpace(ctx.Get(cfg.CaptureHeader))
	if value == "" {
		return false
	}
	token := strings.TrimSpace(cfg.CaptureToken)
	if token != "" {
		return subtle.ConstantTimeCompare([]byte(value), []byte(token)) == 1 ||
			subtle.ConstantTimeCompare([]byte(strings.TrimSpace(ctx.Get(cfg.CaptureHeader+"-Token"))), []byte(token)) == 1
	}
	if isProdEnvironment(cfg.Environment) {
		return false
	}
	return value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes")
}

func captureBody(body []byte, contentType string, cfg ReplayConfig, fullCapture bool) (string, bool, bool) {
	if len(body) == 0 || !isJSONContentType(contentType) {
		return "", false, false
	}
	out := append([]byte(nil), body...)
	if !fullCapture {
		redacted, ok := redactJSON(out, cfg.FieldBlacklist)
		if !ok {
			return "", false, false
		}
		out = redacted
	}
	truncated := false
	if len(out) > cfg.BodyMaxBytes {
		out = out[:cfg.BodyMaxBytes]
		truncated = true
	}
	return string(out), truncated, true
}

func redactJSON(body []byte, fields []string) ([]byte, bool) {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return nil, false
	}
	redactValue(value, fields)
	out, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	return out, true
}

func redactValue(value any, fields []string) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if isSensitiveField(key, fields) {
				v[key] = "***"
				continue
			}
			redactValue(child, fields)
		}
	case []any:
		for _, child := range v {
			redactValue(child, fields)
		}
	}
}

func isSensitiveField(key string, fields []string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, field := range fields {
		field = strings.ToLower(strings.TrimSpace(field))
		if field != "" && strings.Contains(key, field) {
			return true
		}
	}
	return false
}

func isJSONContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil {
		mediaType = strings.TrimSpace(contentType)
	}
	mediaType = strings.ToLower(mediaType)
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func matchesRoute(path string, patterns []string) bool {
	path = strings.ToLower(strings.TrimSpace(path))
	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if strings.HasSuffix(pattern, "*") {
			if strings.HasPrefix(path, strings.TrimSuffix(pattern, "*")) {
				return true
			}
			continue
		}
		if path == pattern {
			return true
		}
	}
	return false
}

func isProdEnvironment(environment string) bool {
	switch strings.ToLower(strings.TrimSpace(environment)) {
	case "prod", "production":
		return true
	default:
		return false
	}
}
