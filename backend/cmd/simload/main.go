// simload drives the real HTTP/WS/SSE stack with N guest requesters and M guest
// responders under configurable churn, then checks streaming consistency
// invariants. Exit code 1 means a hard invariant violation.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"deeperseek/backend/internal/core"
	"deeperseek/backend/internal/httpapi"
)

type scenario struct {
	Name           string
	Requesters     int
	Responders     int
	Duration       time.Duration
	CancelProb     float64
	SkipProb       float64
	DropProb       float64 // responder disconnects mid-answer
	ChurnProb      float64 // responder goes offline/online between answers
	BlockingRatio  float64 // portion of requesters using stream=false
	UseFallback    bool
	SlowReaderMode bool // scenario f: raw-socket stalled reader probing fragment drops
}

var scenarios = map[string]scenario{
	"a": {Name: "a-balanced", Requesters: 30, Responders: 10, Duration: 30 * time.Second},
	"b": {Name: "b-queue-pressure", Requesters: 50, Responders: 5, Duration: 40 * time.Second, BlockingRatio: 0.1},
	"c": {Name: "c-idle-responders", Requesters: 10, Responders: 20, Duration: 25 * time.Second},
	"d": {Name: "d-heavy-churn", Requesters: 40, Responders: 12, Duration: 45 * time.Second, CancelProb: 0.12, SkipProb: 0.15, DropProb: 0.15, ChurnProb: 0.35},
	"e": {Name: "e-fallback-only", Requesters: 12, Responders: 0, Duration: 30 * time.Second, UseFallback: true},
	"f": {Name: "f-slow-reader-probe", Requesters: 1, Responders: 1, Duration: 30 * time.Second, SlowReaderMode: true},
}

func main() {
	which := flag.String("scenario", "all", "scenario key (a|b|c|d|e|f|all)")
	target := flag.String("target", "", "external base URL; empty starts an in-process server")
	duration := flag.Duration("duration", 0, "override scenario load duration")
	flag.Parse()

	keys := []string{"a", "b", "c", "d", "e", "f"}
	if *which != "all" {
		keys = strings.Split(*which, ",")
	}

	hardFailures := 0
	for _, key := range keys {
		sc, ok := scenarios[strings.TrimSpace(key)]
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown scenario %q\n", key)
			os.Exit(2)
		}
		if *duration > 0 {
			sc.Duration = *duration
		}
		report := runScenario(sc, *target)
		report.print()
		hardFailures += len(report.Violations)
	}
	if hardFailures > 0 {
		os.Exit(1)
	}
}

func runScenario(sc scenario, target string) *report {
	fmt.Printf("\n=== scenario %s: N=%d requesters, M=%d responders, %s ===\n", sc.Name, sc.Requesters, sc.Responders, sc.Duration)

	base := target
	var shutdown func()
	if base == "" {
		base, shutdown = startInProcessServer(sc)
		defer shutdown()
	}

	world := newWorld(sc, base)
	ctx, cancel := context.WithCancel(context.Background())

	for i := 0; i < sc.Responders; i++ {
		world.wg.Add(1)
		go world.runResponder(ctx, i)
	}
	// slight head start so the first questions do not all race an empty pool
	time.Sleep(300 * time.Millisecond)
	for i := 0; i < sc.Requesters; i++ {
		world.wg.Add(1)
		world.requesterWG.Add(1)
		go world.runRequester(ctx, i)
	}

	time.Sleep(sc.Duration)
	world.stopAsking.Store(true)
	// drain: give in-flight answers time to finish before tearing responders down
	world.requesterWG.Wait()
	cancel()
	world.wg.Wait()

	return world.check()
}

func startInProcessServer(sc scenario) (string, func()) {
	svc := core.NewService()
	options := httpapi.ServerOptions{}
	var mock *mockUpstream
	if sc.UseFallback {
		mock = startMockUpstream()
		options.Fallback = httpapi.FallbackConfig{
			Enabled:       true,
			BaseURL:       mock.url,
			APIKey:        "sim-key",
			Model:         "mock-model",
			Delay:         2 * time.Second,
			ChunkDelay:    15 * time.Millisecond,
			MaxChunkRunes: 5,
		}
	}
	server := httpapi.NewServerWithOptions(svc, options)
	sweepCtx, stopSweep := context.WithCancel(context.Background())
	go svc.RunTimeoutSweeper(sweepCtx, time.Second)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	httpServer := &http.Server{Handler: server.Handler()}
	go func() { _ = httpServer.Serve(listener) }()

	return "http://" + listener.Addr().String(), func() {
		stopSweep()
		_ = httpServer.Close()
		if mock != nil {
			mock.close()
		}
	}
}

type report struct {
	Scenario    string
	Requests    int
	Completed   int
	Cancelled   int
	Stalled     []string
	Mismatches  []string
	Overlaps    []string
	Violations  []string // hard failures (subset of the above, formatted)
	Probes      []string // informational, environment-sensitive
	FirstDelta  []time.Duration
	MaxGap      time.Duration
	AckErrors   map[string]int
	Assignments int
}

func (r *report) print() {
	fmt.Printf("requests=%d completed=%d cancelled=%d stalled=%d assignments=%d\n",
		r.Requests, r.Completed, r.Cancelled, len(r.Stalled), r.Assignments)
	if len(r.FirstDelta) > 0 {
		sort.Slice(r.FirstDelta, func(i, j int) bool { return r.FirstDelta[i] < r.FirstDelta[j] })
		p := func(q float64) time.Duration { return r.FirstDelta[int(q*float64(len(r.FirstDelta)-1))] }
		fmt.Printf("time-to-first-delta p50=%s p95=%s max=%s | max inter-delta gap=%s\n",
			p(0.5).Round(time.Millisecond), p(0.95).Round(time.Millisecond), p(1).Round(time.Millisecond), r.MaxGap.Round(time.Millisecond))
	}
	if len(r.AckErrors) > 0 {
		fmt.Printf("responder-observed errors: %v\n", r.AckErrors)
	}
	for _, probe := range r.Probes {
		fmt.Printf("PROBE: %s\n", probe)
	}
	if len(r.Violations) == 0 {
		fmt.Printf("PASS %s\n", r.Scenario)
		return
	}
	for _, v := range r.Violations {
		fmt.Printf("FAIL %s: %s\n", r.Scenario, v)
	}
}
