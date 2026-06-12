package metrics

// Cardinality tests (INV-MET-1 / MET-4) have been moved to
// internal/observability/duemetrics/duemetrics_test.go because the HTTP
// middleware adapter (HTTPMiddleware) now lives in the due adapter package.
// The test names and assertions are identical; only the package path changed.
