package metrics

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

const defaultNamespace = "tsu"

// Config identifies the metric namespace and the service label emitted by this process.
type Config struct {
	Namespace string
	Service   string
}

// Registry owns a process-local Prometheus registry and the TSU service metrics.
type Registry struct {
	*prometheus.Registry

	cfg Config

	httpRequests *prometheus.CounterVec
	httpDuration *prometheus.HistogramVec
	httpInflight *prometheus.GaugeVec

	nodeCalls    *prometheus.CounterVec
	nodeDuration *prometheus.HistogramVec

	gateConnections      *prometheus.GaugeVec
	gateBoundUsers       *prometheus.GaugeVec
	gateMessagesReceived *prometheus.CounterVec
	gateMessagesSent     *prometheus.CounterVec
	gateBytesReceived    *prometheus.CounterVec
	gateBytesSent        *prometheus.CounterVec
	gateConnDuration     *prometheus.HistogramVec
}

var (
	registryMu sync.Mutex
	registries = make(map[Config]*Registry)
)

// Init returns the idempotent metrics registry for one namespace/service pair.
func Init(cfg Config) *Registry {
	cfg = normalizeConfig(cfg)

	registryMu.Lock()
	defer registryMu.Unlock()

	if existing := registries[cfg]; existing != nil {
		return existing
	}
	reg := newRegistry(cfg)
	registries[cfg] = reg
	return reg
}

func newRegistry(cfg Config) *Registry {
	promReg := prometheus.NewRegistry()
	prefixed := prometheus.WrapRegistererWithPrefix(
		cfg.Namespace+"_",
		prometheus.WrapRegistererWith(prometheus.Labels{"service": cfg.Service}, promReg),
	)
	mustRegister(prefixed,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	r := &Registry{
		Registry: promReg,
		cfg:      cfg,
		httpRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: cfg.Namespace,
			Subsystem: "http",
			Name:      "requests_total",
			Help:      "Total HTTP requests handled by TSU services.",
		}, []string{"service", "route", "method", "status_class"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: cfg.Namespace,
			Subsystem: "http",
			Name:      "request_duration_seconds",
			Help:      "HTTP request duration in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"service", "route", "method"}),
		httpInflight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: cfg.Namespace,
			Subsystem: "http",
			Name:      "inflight_requests",
			Help:      "Current in-flight HTTP requests.",
		}, []string{"service"}),
		nodeCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: cfg.Namespace,
			Subsystem: "node",
			Name:      "route_calls_total",
			Help:      "Total due node route calls handled by TSU services.",
		}, []string{"service", "route", "status"}),
		nodeDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: cfg.Namespace,
			Subsystem: "node",
			Name:      "route_duration_seconds",
			Help:      "Due node route duration in seconds.",
			Buckets:   []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5},
		}, []string{"service", "route"}),
		gateConnections: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: cfg.Namespace,
			Subsystem: "gate",
			Name:      "connections",
			Help:      "Current open gate network connections.",
		}, []string{"service"}),
		gateBoundUsers: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: cfg.Namespace,
			Subsystem: "gate",
			Name:      "bound_users",
			Help:      "Current gate connections bound to a non-zero user ID.",
		}, []string{"service"}),
		gateMessagesReceived: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: cfg.Namespace,
			Subsystem: "gate",
			Name:      "messages_received_total",
			Help:      "Total gate messages received from clients.",
		}, []string{"service"}),
		gateMessagesSent: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: cfg.Namespace,
			Subsystem: "gate",
			Name:      "messages_sent_total",
			Help:      "Total gate messages sent to clients.",
		}, []string{"service"}),
		gateBytesReceived: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: cfg.Namespace,
			Subsystem: "gate",
			Name:      "bytes_received_total",
			Help:      "Total gate payload bytes received from clients.",
		}, []string{"service"}),
		gateBytesSent: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: cfg.Namespace,
			Subsystem: "gate",
			Name:      "bytes_sent_total",
			Help:      "Total gate payload bytes sent to clients.",
		}, []string{"service"}),
		gateConnDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: cfg.Namespace,
			Subsystem: "gate",
			Name:      "connection_duration_seconds",
			Help:      "Gate network connection lifetime in seconds.",
			Buckets:   []float64{1, 10, 60, 600, 3600, 21600},
		}, []string{"service"}),
	}
	mustRegister(promReg,
		r.httpRequests,
		r.httpDuration,
		r.httpInflight,
		r.nodeCalls,
		r.nodeDuration,
		r.gateConnections,
		r.gateBoundUsers,
		r.gateMessagesReceived,
		r.gateMessagesSent,
		r.gateBytesReceived,
		r.gateBytesSent,
		r.gateConnDuration,
	)
	return r
}

func normalizeConfig(cfg Config) Config {
	cfg.Namespace = strings.TrimSpace(cfg.Namespace)
	if cfg.Namespace == "" {
		cfg.Namespace = defaultNamespace
	}
	cfg.Service = strings.TrimSpace(cfg.Service)
	if cfg.Service == "" {
		cfg.Service = "unknown"
	}
	return cfg
}

func mustRegister(reg prometheus.Registerer, collectors ...prometheus.Collector) {
	for _, collector := range collectors {
		if err := reg.Register(collector); err != nil {
			if _, ok := err.(prometheus.AlreadyRegisteredError); ok {
				continue
			}
			panic(err)
		}
	}
}

// --- due-free exported recording primitives ---
// These methods expose only stdlib/neutral types so that due adapter packages
// in internal/ can call them without pulling due into the core.

// ObserveNodeRoute records one node route call with its status and duration.
// route is the numeric due route ID; status must be "ok" or "error".
func (r *Registry) ObserveNodeRoute(route int32, status string, d time.Duration) {
	if status != "error" {
		status = "ok"
	}
	routeLabel := strconv.FormatInt(int64(route), 10)
	r.nodeCalls.WithLabelValues(r.cfg.Service, routeLabel, status).Inc()
	r.nodeDuration.WithLabelValues(r.cfg.Service, routeLabel).Observe(d.Seconds())
}

// ObserveNodeMiddleware runs next(), captures panic/ok status, then calls
// ObserveNodeRoute. It re-panics after recording so the caller's recover still fires.
func (r *Registry) ObserveNodeMiddleware(route int32, next func()) {
	start := time.Now()
	status := "ok"
	defer func() {
		if rec := recover(); rec != nil {
			status = "error"
			r.ObserveNodeRoute(route, status, time.Since(start))
			panic(rec)
		}
		r.ObserveNodeRoute(route, status, time.Since(start))
	}()
	next()
}

// GateConnectionInc increments the open-connections gauge by 1.
func (r *Registry) GateConnectionInc() {
	r.gateConnections.WithLabelValues(r.cfg.Service).Inc()
}

// GateConnectionDec decrements the open-connections gauge by 1.
func (r *Registry) GateConnectionDec() {
	r.gateConnections.WithLabelValues(r.cfg.Service).Dec()
}

// GateBoundUserInc increments the bound-users gauge by 1.
func (r *Registry) GateBoundUserInc() {
	r.gateBoundUsers.WithLabelValues(r.cfg.Service).Inc()
}

// GateBoundUserDec decrements the bound-users gauge by 1.
func (r *Registry) GateBoundUserDec() {
	r.gateBoundUsers.WithLabelValues(r.cfg.Service).Dec()
}

// GateMessageReceived records one inbound message with its payload size in bytes.
func (r *Registry) GateMessageReceived(bytes int) {
	r.gateMessagesReceived.WithLabelValues(r.cfg.Service).Inc()
	r.gateBytesReceived.WithLabelValues(r.cfg.Service).Add(float64(bytes))
}

// GateMessageSent records one outbound message with its payload size in bytes.
func (r *Registry) GateMessageSent(bytes int) {
	r.gateMessagesSent.WithLabelValues(r.cfg.Service).Inc()
	r.gateBytesSent.WithLabelValues(r.cfg.Service).Add(float64(bytes))
}

// GateConnDurationObserve records a connection lifetime duration.
func (r *Registry) GateConnDurationObserve(d time.Duration) {
	r.gateConnDuration.WithLabelValues(r.cfg.Service).Observe(d.Seconds())
}

// ObserveHTTPRequest records one HTTP request outcome.
// route should be the route template (e.g. "/users/:id"), not the actual path.
// method is the HTTP verb. statusCode is the response status code.
func (r *Registry) ObserveHTTPRequest(route, method string, statusCode int, d time.Duration) {
	r.httpRequests.WithLabelValues(r.cfg.Service, route, method, StatusClass(statusCode)).Inc()
	r.httpDuration.WithLabelValues(r.cfg.Service, route, method).Observe(d.Seconds())
}

// HTTPInflightInc increments the in-flight HTTP requests gauge by 1.
func (r *Registry) HTTPInflightInc() {
	r.httpInflight.WithLabelValues(r.cfg.Service).Inc()
}

// HTTPInflightDec decrements the in-flight HTTP requests gauge by 1.
func (r *Registry) HTTPInflightDec() {
	r.httpInflight.WithLabelValues(r.cfg.Service).Dec()
}

// StatusClass maps an HTTP status code to its bucket string ("1xx"–"5xx" or "unknown").
func StatusClass(status int) string {
	if status >= 100 && status <= 599 {
		return strconv.Itoa(status/100) + "xx"
	}
	return "unknown"
}

// Service returns the service label value configured for this registry.
func (r *Registry) Service() string { return r.cfg.Service }

// Config returns the normalized Config used to create this registry.
func (r *Registry) Config() Config { return r.cfg }

func resetForTest() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registries = make(map[Config]*Registry)
}
