package logging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func TestMain(m *testing.M) {
	nowFunc = func() time.Time { return time.Date(2026, 5, 30, 12, 0, 0, 123, time.UTC) }
	code := m.Run()
	nowFunc = func() time.Time { return time.Now().UTC() }
	os.Exit(code)
}

func TestStepAddsSpanEventAndWritesSink(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(recorder),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	ctx, span := tp.Tracer("test").Start(context.Background(), "request")
	sink := &recordingSink{}
	restore := SetSinks(sink)
	defer restore()

	Step(ctx, "load hero", "hero_id", int64(7), "ok", true)
	span.End()

	if len(sink.entries) != 1 {
		t.Fatalf("sink entries = %d, want 1", len(sink.entries))
	}
	if sink.entries[0].Level != LevelStep || sink.entries[0].Message != "load hero" {
		t.Fatalf("unexpected sink entry: %#v", sink.entries[0])
	}
	if sink.entries[0].TraceID == "" {
		t.Fatal("sink trace id is empty")
	}
	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	events := spans[0].Events()
	if len(events) != 1 || events[0].Name != "load hero" {
		t.Fatalf("unexpected events: %#v", events)
	}
	attrs := attrsMap(events[0].Attributes)
	if attrs["tsu.log.level"] != "step" || attrs["tsu.log.hero_id"] != "7" || attrs["tsu.log.ok"] != "true" {
		t.Fatalf("unexpected event attrs: %#v", attrs)
	}
}

func TestNoActiveSpanOnlyWritesSink(t *testing.T) {
	sink := &recordingSink{}
	restore := SetSinks(sink)
	defer restore()

	Info(context.Background(), "background job", "job", "sync")

	if len(sink.entries) != 1 {
		t.Fatalf("sink entries = %d, want 1", len(sink.entries))
	}
	if sink.entries[0].TraceID != "" {
		t.Fatalf("trace id = %q, want empty", sink.entries[0].TraceID)
	}
}

func TestOddKVAndNilContextDoNotPanic(t *testing.T) {
	sink := &recordingSink{}
	restore := SetSinks(sink)
	defer restore()

	Warn(nil, "odd", "key-only")

	if len(sink.entries) != 1 {
		t.Fatalf("sink entries = %d, want 1", len(sink.entries))
	}
	if len(sink.entries[0].Fields) != 1 || sink.entries[0].Fields[0].Key != "key-only" {
		t.Fatalf("unexpected fields: %#v", sink.entries[0].Fields)
	}
}

func TestErrorLevelAndAttributeTypeBranches(t *testing.T) {
	sink := &recordingSink{}
	restore := SetSinks(sink)
	defer restore()

	Error(context.Background(), "failed", "name", "alice", "count", 3, "ratio", 1.5, "custom", stringerValue("ok"), "fallback", struct{ ID int }{ID: 9})

	if len(sink.entries) != 1 {
		t.Fatalf("sink entries = %d, want 1", len(sink.entries))
	}
	if sink.entries[0].Level != LevelError {
		t.Fatalf("level = %s, want %s", sink.entries[0].Level, LevelError)
	}
	attrs := attrsMap(attributesFromEntry(sink.entries[0]))
	for key, want := range map[string]string{
		"tsu.log.level":    "error",
		"tsu.log.name":     "alice",
		"tsu.log.count":    "3",
		"tsu.log.ratio":    "1.5",
		"tsu.log.custom":   "ok",
		"tsu.log.fallback": "{9}",
	} {
		if attrs[key] != want {
			t.Fatalf("%s = %q, want %q in %#v", key, attrs[key], want, attrs)
		}
	}
}

func TestErrorFieldValuesAreStringified(t *testing.T) {
	sink := &recordingSink{}
	restore := SetSinks(sink)
	defer restore()

	wrapped := fmt.Errorf("读取冻结配置失败: %w", errors.New("boom"))
	Error(context.Background(), "failed", "error", wrapped, "cause", errors.New("boom"))

	if len(sink.entries) != 1 {
		t.Fatalf("sink entries = %d, want 1", len(sink.entries))
	}
	fields := sink.entries[0].Fields
	if len(fields) != 2 {
		t.Fatalf("fields = %d, want 2: %#v", len(fields), fields)
	}
	if fields[0].Value != "读取冻结配置失败: boom" {
		t.Fatalf("error field = %#v, want unwrapped Error() text", fields[0].Value)
	}
	if fields[1].Value != "boom" {
		t.Fatalf("cause field = %#v, want %q", fields[1].Value, "boom")
	}

	line, ok := formatJSONEntry(sink.entries[0])
	if !ok {
		t.Fatal("formatJSONEntry returned !ok")
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(line), &doc); err != nil {
		t.Fatalf("json output invalid: %v: %s", err, line)
	}
	if doc["error"] != "读取冻结配置失败: boom" {
		t.Fatalf(`doc["error"] = %#v, want Error() text (regression: was {})`, doc["error"])
	}
}

func TestTypedNilErrorFieldDoesNotPanic(t *testing.T) {
	sink := &recordingSink{}
	restore := SetSinks(sink)
	defer restore()

	var typedNil *nilProneError
	Error(context.Background(), "failed", "error", typedNil)

	if len(sink.entries) != 1 {
		t.Fatalf("sink entries = %d, want 1", len(sink.entries))
	}
	if sink.entries[0].Fields[0].Value != "<nil>" {
		t.Fatalf("typed-nil error field = %#v, want %q", sink.entries[0].Fields[0].Value, "<nil>")
	}
}

func TestFormatJSONEntryIncludesBaseFieldsTraceAndKV(t *testing.T) {
	SetServiceName("game-api")
	got, ok := formatJSONEntry(Entry{
		Level:   LevelError,
		Message: "failed",
		TraceID: traceID(1).String(),
		Fields:  []Field{{Key: "bad key", Value: "value"}},
	})
	if !ok {
		t.Fatal("formatJSONEntry returned !ok")
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("json output invalid: %v: %s", err, got)
	}
	for key, want := range map[string]string{
		"ts":       "2026-05-30T12:00:00.000000123Z",
		"level":    "error",
		"service":  "game-api",
		"msg":      "failed",
		"trace_id": traceID(1).String(),
		"bad_key":  "value",
	} {
		if doc[key] != want {
			t.Fatalf("%s = %#v, want %q in %s", key, doc[key], want, got)
		}
	}
}

func TestFormatJSONEntryWithoutActiveSpanKeepsEmptyTraceID(t *testing.T) {
	got, ok := formatJSONEntry(Entry{Level: LevelInfo, Message: "background"})
	if !ok {
		t.Fatal("formatJSONEntry returned !ok")
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("json output invalid: %v: %s", err, got)
	}
	if doc["trace_id"] != "" {
		t.Fatalf("trace_id = %#v, want empty", doc["trace_id"])
	}
}

func TestFormatJSONEntryFallsBackOnMarshalError(t *testing.T) {
	got, ok := formatJSONEntry(Entry{
		Level:   LevelWarn,
		Message: "bad",
		Fields:  []Field{{Key: "fn", Value: func() {}}},
	})
	if ok || got != "" {
		t.Fatalf("formatJSONEntry = %q, %v; want empty false", got, ok)
	}
	fallback := fallbackTextEntry(Entry{
		Level:   LevelWarn,
		Message: "bad",
		Fields:  []Field{{Key: "bad key", Value: func() {}}},
	})
	if fallback == "" {
		t.Fatal("fallback text is empty")
	}
}

func TestStdoutJSONSinkWritesJSONAndFallbackLines(t *testing.T) {
	SetServiceName("game-api")
	jsonOut := captureStdout(t, func() {
		if err := (stdoutJSONSink{}).Write(context.Background(), Entry{Level: LevelInfo, Message: "sink line"}); err != nil {
			t.Fatalf("Write json: %v", err)
		}
	})
	if !strings.Contains(jsonOut, `"service":"game-api"`) || !strings.Contains(jsonOut, `"msg":"sink line"`) {
		t.Fatalf("unexpected json sink output: %q", jsonOut)
	}

	fallbackOut := captureStdout(t, func() {
		if err := (stdoutJSONSink{}).Write(context.Background(), Entry{
			Level:   LevelWarn,
			Message: "fallback",
			Fields:  []Field{{Key: "bad key", Value: func() {}}},
		}); err != nil {
			t.Fatalf("Write fallback: %v", err)
		}
	})
	if !strings.Contains(fallbackOut, "obs level=warn") || !strings.Contains(fallbackOut, "bad_key=") {
		t.Fatalf("unexpected fallback sink output: %q", fallbackOut)
	}
}

func TestConfigureProcessSetsServiceForStdLogWriter(t *testing.T) {
	ConfigureProcess("cockpit-api")
	writer := structuredStdLogWriter{}
	if n, err := writer.Write([]byte(stdLogPrefix + "hello\n")); err != nil || n == 0 {
		t.Fatalf("std log writer Write = %d, %v", n, err)
	}
}

type recordingSink struct {
	entries []Entry
}

func (s *recordingSink) Write(_ context.Context, entry Entry) error {
	s.entries = append(s.entries, entry)
	return nil
}

type stringerValue string

func (s stringerValue) String() string {
	return string(s)
}

// nilProneError 的 Error() 解引用接收者：typed-nil 直接调用会 panic，
// 用于验证 normalizeFieldValue 的反射判 nil 防护。
type nilProneError struct {
	msg string
}

func (e *nilProneError) Error() string {
	return e.msg
}

func attrsMap(attrs []attribute.KeyValue) map[string]string {
	out := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		out[string(attr.Key)] = attr.Value.Emit()
	}
	return out
}

func traceID(seed byte) oteltrace.TraceID {
	var id oteltrace.TraceID
	for i := range id {
		id[i] = seed + byte(i)
	}
	return id
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	previous := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = writer
	defer func() {
		os.Stdout = previous
	}()
	fn()
	if err := writer.Close(); err != nil {
		t.Fatalf("close pipe writer: %v", err)
	}
	out, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return string(out)
}
