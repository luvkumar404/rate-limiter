package metrics

import (
	"context"
	"net/http"
	"time"

	"github.com/luvkumar404/rate-limiter/internal/limiter"
	"github.com/luvkumar404/rate-limiter/internal/resilience"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics owns one server's collectors. It never registers global collectors
// or uses caller-controlled label values.
type Metrics struct {
	registry    *prometheus.Registry
	checks      *prometheus.CounterVec
	duration    prometheus.Histogram
	redisErrors prometheus.Counter
	handler     http.Handler
}

func New(state func() resilience.State) *Metrics {
	m := &Metrics{
		registry:    prometheus.NewRegistry(),
		checks:      prometheus.NewCounterVec(prometheus.CounterOpts{Name: "checks_total", Help: "Completed limiter calls by outcome; excludes request validation and health probes."}, []string{"result"}),
		duration:    prometheus.NewHistogram(prometheus.HistogramOpts{Name: "check_duration_seconds", Help: "Limiter call duration including backend waits and failure policy, excluding HTTP parsing and writes.", Buckets: []float64{.0001, .00025, .0005, .001, .0025, .005, .01, .025, .05, .1, .25, .5, 1}}),
		redisErrors: prometheus.NewCounter(prometheus.CounterOpts{Name: "redis_errors_total", Help: "Failed logical Redis checks and pings; excludes caller cancellation and circuit short-circuits."}),
	}
	for _, result := range []string{"allowed", "blocked", "degraded", "error", "canceled"} {
		m.checks.WithLabelValues(result)
	}
	circuit := prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "circuit_state", Help: "Redis circuit state: 0=closed (also memory mode), 1=open, 2=half-open."}, func() float64 {
		if state != nil {
			switch state() {
			case resilience.Open:
				return 1
			case resilience.HalfOpen:
				return 2
			}
		}
		return 0
	})
	m.registry.MustRegister(m.checks, m.duration, m.redisErrors, circuit, collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	m.handler = promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
	return m
}

func (m *Metrics) Handler() http.Handler { return m.handler }

// ObserveCheck records exactly one completed Allow call, including fallback.
func (m *Metrics) ObserveCheck(ctx context.Context, result limiter.Result, err error, elapsed time.Duration) {
	outcome := "allowed"
	switch {
	case err != nil && ctx.Err() != nil:
		outcome = "canceled"
	case err != nil:
		outcome = "error"
	case result.Degraded:
		outcome = "degraded"
	case !result.Allowed:
		outcome = "blocked"
	}
	m.checks.WithLabelValues(outcome).Inc()
	m.duration.Observe(elapsed.Seconds())
}

func (m *Metrics) RedisOperation(ctx context.Context, call func(context.Context) error) error {
	err := call(ctx)
	if err != nil && ctx.Err() == nil {
		m.redisErrors.Inc()
	}
	return err
}

type redisLimiter struct {
	next    limiter.Limiter
	metrics *Metrics
}

// WrapRedis belongs inside the failure guard so only actual Redis errors count.
func (m *Metrics) WrapRedis(next limiter.Limiter) limiter.Limiter {
	return &redisLimiter{next: next, metrics: m}
}
func (l *redisLimiter) Allow(ctx context.Context, key string) (limiter.Result, error) {
	var result limiter.Result
	err := l.metrics.RedisOperation(ctx, func(ctx context.Context) error { var err error; result, err = l.next.Allow(ctx, key); return err })
	return result, err
}
