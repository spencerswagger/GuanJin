package observability

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	DependencyDB    = "db"
	DependencyRedis = "redis"
	DependencyKeys  = "keys"
)

type Metrics struct {
	Registry     *prometheus.Registry
	DependencyUp *prometheus.GaugeVec
}

func NewMetrics() *Metrics {
	registry := prometheus.NewRegistry()
	dependencyUp := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "guanjin_dependency_up",
		Help: "Reachability of a hard dependency (db/redis/keys); 1 up, 0 down.",
	}, []string{"dep"})
	registry.MustRegister(dependencyUp)
	return &Metrics{Registry: registry, DependencyUp: dependencyUp}
}

func (m *Metrics) SetDependencyUp(dep string, up bool) {
	v := 0.0
	if up {
		v = 1.0
	}
	m.DependencyUp.WithLabelValues(dep).Set(v)
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}
