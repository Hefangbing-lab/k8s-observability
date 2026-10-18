package main

import (
	"encoding/json"
	"log"
	"net/http"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", healthHandler)
	mux.HandleFunc("GET /order/{id}", orderHandler)

	log.Println("order-service starting on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
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
