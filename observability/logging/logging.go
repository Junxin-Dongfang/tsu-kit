// Package logging is a framework-neutral structured logging core: leveled
// Step/Info/Warn/Error calls fan out to pluggable Sinks and (when a span is
// active) attach as span events. It imports no service framework, so any Go
// service can depend on it directly. Framework-specific wiring (e.g. routing a
// framework's own internal logger into this stream) belongs in an adapter that
// imports this package — see internal/observability/duelog for the due adapter.
package logging

import (
	"context"
	"encoding/json"
	"fmt"
	stdlog "log"
	"os"
	"reflect"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

type Level string

const (
	LevelStep  Level = "step"
	LevelInfo  Level = "info"
	LevelWarn  Level = "warn"
	LevelError Level = "error"
)

type Entry struct {
	Level   Level
	Message string
	TraceID string
	Fields  []Field
}

type Field struct {
	Key   string
	Value any
}

type Sink interface {
	Write(context.Context, Entry) error
}

var (
	sinksMu sync.RWMutex
	sinks   = []Sink{stdoutJSONSink{}}

	serviceMu     sync.RWMutex
	serviceName   string
	nowFunc       = func() time.Time { return time.Now().UTC() }
	stdLogPrefix  = "[std] "
	fallbackMutex sync.Mutex
)

func SetSinks(next ...Sink) func() {
	sinksMu.Lock()
	previous := sinks
	sinks = append([]Sink(nil), next...)
	sinksMu.Unlock()
	return func() {
		sinksMu.Lock()
		sinks = previous
		sinksMu.Unlock()
	}
}

func SetServiceName(name string) {
	serviceMu.Lock()
	serviceName = strings.TrimSpace(name)
	serviceMu.Unlock()
}

// ConfigureProcess sets the process service name and redirects the standard
// library logger into this structured JSON stream. It is framework-neutral;
// wiring a specific framework's internal logger is the caller/adapter's concern.
func ConfigureProcess(service string) {
	SetServiceName(service)
	stdlog.SetFlags(0)
	stdlog.SetPrefix(stdLogPrefix)
	stdlog.SetOutput(structuredStdLogWriter{})
}

func Step(ctx context.Context, msg string, kv ...any) {
	write(ctx, LevelStep, msg, kv...)
}

func Info(ctx context.Context, msg string, kv ...any) {
	write(ctx, LevelInfo, msg, kv...)
}

func Warn(ctx context.Context, msg string, kv ...any) {
	write(ctx, LevelWarn, msg, kv...)
}

func Error(ctx context.Context, msg string, kv ...any) {
	write(ctx, LevelError, msg, kv...)
}

func write(ctx context.Context, level Level, msg string, kv ...any) {
	if ctx == nil {
		ctx = context.Background()
	}
	fields := fieldsFromKV(kv...)
	traceID := ""
	span := oteltrace.SpanFromContext(ctx)
	if sc := span.SpanContext(); sc.IsValid() && sc.TraceID().IsValid() {
		traceID = sc.TraceID().String()
	}
	entry := Entry{Level: level, Message: strings.TrimSpace(msg), TraceID: traceID, Fields: fields}
	if span.IsRecording() {
		attrs := attributesFromEntry(entry)
		span.AddEvent(entry.Message, oteltrace.WithAttributes(attrs...))
	}

	sinksMu.RLock()
	targets := append([]Sink(nil), sinks...)
	sinksMu.RUnlock()
	for _, sink := range targets {
		if sink != nil {
			_ = sink.Write(ctx, entry)
		}
	}
}

func fieldsFromKV(kv ...any) []Field {
	fields := make([]Field, 0, (len(kv)+1)/2)
	for i := 0; i < len(kv); i += 2 {
		key := fmt.Sprintf("kv_%d", i)
		if s, ok := kv[i].(string); ok && strings.TrimSpace(s) != "" {
			key = strings.TrimSpace(s)
		}
		var value any
		if i+1 < len(kv) {
			value = normalizeFieldValue(kv[i+1])
		}
		fields = append(fields, Field{Key: key, Value: value})
	}
	return fields
}

// normalizeFieldValue 把 error 值归一化为其 Error() 文本。encoding/json 对不实现
// json.Marshaler 且无导出字段的 error（errors.New / fmt.Errorf / 各服务的领域错误
// 类型）一律序列化为 {}，JSON sink 会丢失全部错误详情。在 KV 收口点统一转换，
// JSON sink / span event / fallback text 三路输出一致受益。
//
// typed-nil error（如把 (*SomeError)(nil) 塞进 interface）直接调 Error() 可能
// panic，经反射判 nil 后回落 "<nil>"。非 error 值原样透传，不改变既有行为。
func normalizeFieldValue(value any) any {
	err, ok := value.(error)
	if !ok {
		return value
	}
	if isNilValue(err) {
		return "<nil>"
	}
	return err.Error()
}

// isNilValue 判定 interface 内包裹的具体值是否为 nil（typed-nil 防护）。
func isNilValue(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
		return rv.IsNil()
	default:
		return false
	}
}

func attributesFromEntry(entry Entry) []attribute.KeyValue {
	attrs := []attribute.KeyValue{
		attribute.String("tsu.log.level", string(entry.Level)),
	}
	if entry.TraceID != "" {
		attrs = append(attrs, attribute.String("trace_id", entry.TraceID))
	}
	for _, field := range entry.Fields {
		attrs = append(attrs, attributeFromField(field))
	}
	return attrs
}

func attributeFromField(field Field) attribute.KeyValue {
	key := attribute.Key("tsu.log." + sanitizeKey(field.Key))
	switch value := field.Value.(type) {
	case string:
		return key.String(value)
	case bool:
		return key.Bool(value)
	case int:
		return key.Int(value)
	case int64:
		return key.Int64(value)
	case float64:
		return key.Float64(value)
	case fmt.Stringer:
		return key.String(value.String())
	default:
		return key.String(fmt.Sprint(value))
	}
}

func sanitizeKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// WriteLine writes a fully-formed log line to the process log output (stdout),
// appending a trailing newline when absent. Sink and adapter authors use it to
// emit records that bypass the Entry pipeline.
func WriteLine(line string) {
	writeFallbackLine(line)
}

// NewDocument builds the base structured log document shared by every sink
// (ts, level, service, msg, trace_id). A zero ts is replaced with the current
// time. Adapter authors enrich the returned map with extra fields before
// marshalling.
func NewDocument(ts time.Time, level Level, msg, traceID string) map[string]any {
	return newLogDocumentAt(ts, level, msg, traceID)
}

type stdoutJSONSink struct{}

func (stdoutJSONSink) Write(_ context.Context, entry Entry) error {
	line, ok := formatJSONEntry(entry)
	if !ok {
		line = fallbackTextEntry(entry)
	}
	writeFallbackLine(line)
	return nil
}

func formatJSONEntry(entry Entry) (string, bool) {
	doc := newLogDocument(entry.Level, entry.Message, entry.TraceID)
	for _, field := range entry.Fields {
		doc[sanitizeKey(field.Key)] = field.Value
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return "", false
	}
	return string(raw), true
}

func newLogDocument(level Level, msg string, traceID string) map[string]any {
	return newLogDocumentAt(nowFunc(), level, msg, traceID)
}

func newLogDocumentAt(ts time.Time, level Level, msg string, traceID string) map[string]any {
	if ts.IsZero() {
		ts = nowFunc()
	}
	return map[string]any{
		"ts":       ts.UTC().Format(time.RFC3339Nano),
		"level":    string(level),
		"service":  currentServiceName(),
		"msg":      strings.TrimSpace(msg),
		"trace_id": traceID,
	}
}

func currentServiceName() string {
	serviceMu.RLock()
	defer serviceMu.RUnlock()
	return serviceName
}

func fallbackTextEntry(entry Entry) string {
	parts := []string{"obs", "level=" + string(entry.Level)}
	if svc := currentServiceName(); svc != "" {
		parts = append(parts, "service="+svc)
	}
	if entry.TraceID != "" {
		parts = append(parts, "trace_id="+entry.TraceID)
	}
	if entry.Message != "" {
		parts = append(parts, "msg="+entry.Message)
	}
	for _, field := range entry.Fields {
		parts = append(parts, fmt.Sprintf("%s=%v", sanitizeKey(field.Key), field.Value))
	}
	return strings.Join(parts, " ")
}

type structuredStdLogWriter struct{}

func (structuredStdLogWriter) Write(p []byte) (int, error) {
	message := strings.TrimSpace(strings.TrimPrefix(string(p), stdLogPrefix))
	doc := newLogDocument(LevelInfo, message, "")
	raw, err := json.Marshal(doc)
	if err != nil {
		writeFallbackLine(message)
		return len(p), nil
	}
	writeFallbackLine(string(raw))
	return len(p), nil
}

func writeFallbackLine(line string) {
	fallbackMutex.Lock()
	defer fallbackMutex.Unlock()
	_, _ = os.Stdout.WriteString(line)
	if !strings.HasSuffix(line, "\n") {
		_, _ = os.Stdout.WriteString("\n")
	}
}
