package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOrderHandlerCallsDownstreamAndPropagatesRequestID(t *testing.T) {
	registerMetrics()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(requestIDHeader) == "" {
			t.Error("request ID was not propagated to downstream")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"name": "User7", "email": "u7@example.com"})
	}))
	defer upstream.Close()

	old := downstreamBaseURL
	downstreamBaseURL = upstream.URL
	defer func() { downstreamBaseURL = old }()

	srv := httptest.NewServer(newHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/order/100?userId=7")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d", resp.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["userName"] != "User7" || body["orderId"] != "100" {
		t.Fatalf("unexpected body: %v", body)
	}
}

func TestOrderHandlerRejectsInvalidUserID(t *testing.T) {
	registerMetrics()
	srv := httptest.NewServer(newHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/order/1?userId=../../admin")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
}

func TestOrderHandlerMapsUpstreamFailureTo502(t *testing.T) {
	registerMetrics()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusServiceUnavailable)
	}))
	defer upstream.Close()

	old := downstreamBaseURL
	downstreamBaseURL = upstream.URL
	defer func() { downstreamBaseURL = old }()

	srv := httptest.NewServer(newHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/order/1?userId=3")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d", resp.StatusCode)
	}
}

func TestOrderHandlerMapsTimeoutTo504(t *testing.T) {
	registerMetrics()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	oldURL, oldClient := downstreamBaseURL, downstreamClient
	downstreamBaseURL = upstream.URL
	downstreamClient = &http.Client{Timeout: 30 * time.Millisecond}
	defer func() {
		downstreamBaseURL = oldURL
		downstreamClient = oldClient
	}()

	srv := httptest.NewServer(newHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/order/1?userId=3")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("expected 504, got %d", resp.StatusCode)
	}
}
