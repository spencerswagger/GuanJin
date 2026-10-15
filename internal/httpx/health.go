package httpx

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/yourorg/guanjin-next/internal/observability"
)

type CheckFunc func(ctx context.Context) error

type Probes struct {
	Checks  map[string]CheckFunc
	Timeout time.Duration
}

func (p *Probes) HealthzHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeProbeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
}

func (p *Probes) ReadyzHandler(metrics *observability.Metrics) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timeout := p.Timeout
		if timeout <= 0 {
			timeout = 3 * time.Second
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		checks := make(map[string]bool, len(p.Checks))
		allUp := true
		for name, check := range p.Checks {
			up := check(ctx) == nil
			if !up {
				allUp = false
			}
			checks[name] = up
			if metrics != nil {
				metrics.SetDependencyUp(name, up)
			}
		}

		status := http.StatusOK
		if !allUp {
			status = http.StatusServiceUnavailable
		}
		writeProbeJSON(w, status, map[string]any{"checks": checks})
	})
}

func writeProbeJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
