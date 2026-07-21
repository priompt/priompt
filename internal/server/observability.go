package server

import (
	"context"
	"log/slog"

	"net/http"
	"priompt/internal/auth"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/time/rate"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	reqTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "priompt_requests_total",
		Help: "gRPC requests by method and gRPC status code.",
	}, []string{"method", "code"})
	reqDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "priompt_request_duration_seconds",
		Help:    "gRPC request latency by method.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method"})
)

// MetricsHandler serves the Prometheus exposition endpoint (mount at /metrics).
// Point a Prometheus scrape at it; Grafana reads from Prometheus.
func MetricsHandler() http.Handler { return promhttp.Handler() }

// MetricsInterceptor counts every RPC and records its latency. Chain it
// outermost so it observes auth and rate-limit rejections too.
func MetricsInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	start := time.Now()
	resp, err := handler(ctx, req)
	reqTotal.WithLabelValues(info.FullMethod, status.Code(err).String()).Inc()
	reqDuration.WithLabelValues(info.FullMethod).Observe(time.Since(start).Seconds())
	return resp, err
}

// AuditInterceptor writes one structured log line per RPC: who (org scope),
// what (method, uri), and outcome (code, latency). Chain it after AuthInterceptor
// so the org scope is populated.
func AuditInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		scope := auth.Scope(ctx)
		logger.Info("rpc",
			"method", info.FullMethod,
			"org", scope,
			"uri", uriOf(req),
			"code", status.Code(err).String(),
			"ms", time.Since(start).Milliseconds(),
		)
		return resp, err
	}
}

// uriOf pulls the prompt URI out of any request that carries one.
func uriOf(req any) string {
	if u, ok := req.(interface{ GetUri() string }); ok {
		return u.GetUri()
	}
	return ""
}

// RateLimitInterceptor enforces a per-org token-bucket limit. rps<=0 disables it.
// Chain it after AuthInterceptor so the org scope is set.
func RateLimitInterceptor(rps float64, burst int) grpc.UnaryServerInterceptor {
	if rps <= 0 {
		return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
			return h(ctx, req)
		}
	}
	if burst <= 0 {
		burst = int(rps)
		if burst < 1 {
			burst = 1
		}
	}
	var mu sync.Mutex
	// ponytail: unbounded map keyed by org scope; orgs come from the trusted
	// token file, so the key space is small. Swap for an LRU if scopes ever
	// become caller-controlled.
	limiters := map[string]*rate.Limiter{}
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		scope := auth.Scope(ctx)
		mu.Lock()
		lim := limiters[scope]
		if lim == nil {
			lim = rate.NewLimiter(rate.Limit(rps), burst)
			limiters[scope] = lim
		}
		mu.Unlock()
		if !lim.Allow() {
			return nil, status.Errorf(codes.ResourceExhausted, "rate limit exceeded for org %q", scope)
		}
		return handler(ctx, req)
	}
}
