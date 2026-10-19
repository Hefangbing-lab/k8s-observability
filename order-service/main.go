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
	mux.HandleFunc("GET /order/{id}", orderHandler)
	mux.Handle("/metrics", promhttp.Handler())

	var handler http.Handler = mux
	handler = promhttp.InstrumentHandlerDuration(httpDuration, handler)
	handler = promhttp.InstrumentHandlerCounter(httpRequests, handler)

	log.Println("order-service starting on :8080")
	log.Fatal(http.ListenAndServe(":8080", handler))
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func orderHandler(w http.ResponseWriter, r *http.Request) {
	orderID := r.PathValue("id")
	userID := r.URL.Query().Get("userId")
	if userID == "" {
		userID = "1"
	}

	// 通过 K8s Service 的 DNS 名调 user-service
	resp, err := http.Get("http://user-service/user/" + userID)
	if err != nil {
		http.Error(w, "call user-service failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close() // 必须关，否则连接泄漏——面试点

	var user map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&user)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"orderId":  orderID,
		"userName": user["name"],
		"email":    user["email"],
		"product":  "云服务器ECS",
	})
}
