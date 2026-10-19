package main

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	httpRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Number of HTTP requests",
		},
		[]string{"code", "method"},
	)
	httpDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"code", "method"},
	)
)

func main() {
	prometheus.MustRegister(httpRequests, httpDuration)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", healthHandler)
	mux.HandleFunc("GET /user/{id}", userHandler)
	mux.Handle("/metrics", promhttp.Handler())

	var handler http.Handler = mux
	handler = promhttp.InstrumentHandlerDuration(httpDuration, handler)
	handler = promhttp.InstrumentHandlerCounter(httpRequests, handler)

	log.Println("user-service starting on :8080")
	log.Fatal(http.ListenAndServe(":8080", handler))
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func userHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	userData := map[string]string{
		"id":    id,
		"name":  "User" + id,
		"email": "user" + id + "@example.com",
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(userData)
}
