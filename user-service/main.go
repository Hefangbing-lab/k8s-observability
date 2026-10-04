package main

import (
	"encoding/json"
	"log"
	"net/http"
)

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", healthHandler)
	// 新增这一行：注册user路由，{id}是路径参数
	mux.HandleFunc("GET /user/{id}", userHandler)

	log.Println("user-service starting on :8080")
	log.Fatal(http.ListenAndServe(":8080", mux))
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// 新增userHandler函数
func userHandler(w http.ResponseWriter, r *http.Request) {
	// 从url路径取出id参数
	id := r.PathValue("id")

	// 构造返回json数据
	userData := map[string]string{
		"id":    id,
		"name":  "User" + id,
		"email": "user" + id + "@example.com",
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(userData)
}
