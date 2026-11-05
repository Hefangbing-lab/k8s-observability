// 极简压测工具：无第三方依赖，编译成静态二进制，跑在集群内。
// 用途：给 README 提供可复现的容量数据（QPS / P99 / 错误数），
// 避免用本机 port-forward 测出来的数字被单条转发连接限制。
package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"
)

type result struct {
	latencies []time.Duration
	errors    int
	total     int
}

func main() {
	url := flag.String("url", "http://order-service/order/100?userId=3", "target url")
	dur := flag.Duration("duration", 60*time.Second, "test duration")
	conc := flag.Int("concurrency", 50, "concurrent workers")
	flag.Parse()

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        1000,
			MaxIdleConnsPerHost: 1000,
			IdleConnTimeout:     30 * time.Second,
			DisableCompression:  true,
		},
	}

	deadline := time.Now().Add(*dur)
	res := &result{latencies: make([]time.Duration, 0, 1<<16)}
	var mu sync.Mutex
	var wg sync.WaitGroup

	start := time.Now()
	for i := 0; i < *conc; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make([]time.Duration, 0, 4096)
			errs := 0
			for time.Now().Before(deadline) {
				req, err := http.NewRequest(http.MethodGet, *url, nil)
				if err != nil {
					errs++
					continue
				}
				t0 := time.Now()
				resp, err := client.Do(req)
				if err != nil {
					errs++
					continue
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					errs++
				}
				local = append(local, time.Since(t0))
			}
			mu.Lock()
			res.latencies = append(res.latencies, local...)
			res.errors += errs
			mu.Unlock()
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	if len(res.latencies) == 0 {
		fmt.Fprintf(os.Stderr, "no successful request, errors=%d\n", res.errors)
		os.Exit(1)
	}
	sort.Slice(res.latencies, func(i, j int) bool { return res.latencies[i] < res.latencies[j] })
	pct := func(p float64) time.Duration {
		idx := int(float64(len(res.latencies)-1) * p)
		return res.latencies[idx]
	}
	total := len(res.latencies)
	res.total = total

	fmt.Printf("url=%s concurrency=%d duration=%.0fs\n", *url, *conc, elapsed.Seconds())
	fmt.Printf("requests=%d errors=%d rps=%.1f\n", total, res.errors, float64(total)/elapsed.Seconds())
	fmt.Printf("p50=%.2fms p90=%.2fms p99=%.2fms max=%.2fms\n",
		float64(pct(0.50).Microseconds())/1000,
		float64(pct(0.90).Microseconds())/1000,
		float64(pct(0.99).Microseconds())/1000,
		float64(res.latencies[len(res.latencies)-1].Microseconds())/1000,
	)
}
