package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// PromHandler serializes this registry in Prometheus text format.
// This is due-free: it only uses stdlib net/http and the prometheus client.
func (r *Registry) PromHandler() http.Handler {
	return promhttp.HandlerFor(r.Registry, promhttp.HandlerOpts{})
}
