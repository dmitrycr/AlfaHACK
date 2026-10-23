package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Explicit, offline soak; never uses environment API keys or external endpoints.
func TestHTTPSoak(t *testing.T) {
	seconds, e := strconv.Atoi(os.Getenv("LLM_SOAK_SECONDS"))
	if e != nil || seconds == 0 {
		t.Skip("set LLM_SOAK_SECONDS=10..60 for the local HTTP soak")
	}
	if seconds < 10 || seconds > 60 {
		t.Fatal("soak duration must be 10..60 seconds")
	}
	cfg := httpConfig()
	input := httpFixture()
	var serverRequests, attempts atomic.Int64
	p, _ := localHTTP(t, cfg, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()
		n := serverRequests.Add(1)
		if n%31 == 0 {
			select {
			case <-r.Context().Done():
			case <-time.After(100 * time.Millisecond):
			}
			return
		}
		if n%13 == 0 {
			w.WriteHeader(429)
			_, _ = io.WriteString(w, `{"error":"rate limit"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(responseFor(input))
	})
	for i := 0; i < 100; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		_, _, _ = p.Complete(ctx, input)
		cancel()
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	startG := runtime.NumGoroutine()
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(seconds)*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(5 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					call, stop := context.WithTimeout(ctx, 25*time.Millisecond)
					_, _, _ = p.Complete(call, input)
					stop()
					attempts.Add(1)
				}
			}
		}()
	}
	samples := []uint64{}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
sampling:
	for {
		select {
		case <-ctx.Done():
			break sampling
		case <-ticker.C:
			runtime.GC()
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			samples = append(samples, m.HeapAlloc)
		}
	}
	wg.Wait()
	p.Close()
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > startG+2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if attempts.Load() < 100 || after.HeapAlloc > before.HeapAlloc+(16<<20) || runtime.NumGoroutine() > startG+2 {
		t.Fatal("soak resource bounds exceeded", attempts.Load(), before.HeapAlloc, after.HeapAlloc, startG, runtime.NumGoroutine())
	}
	if len(samples) >= 2 && samples[len(samples)-1] > samples[len(samples)-2]+(4<<20) {
		t.Fatal("late retained heap growth", samples)
	}
	t.Logf("duration=%ds workers=4 attempts=%d local_server_requests=%d retained_heap_before=%d after=%d samples=%v goroutines_before=%d after=%d", seconds, attempts.Load(), serverRequests.Load(), before.HeapAlloc, after.HeapAlloc, samples, startG, runtime.NumGoroutine())
}
