package api

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/jotabe12/cc65-tb2-grupo3/internal/storage"
)

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.metrics(r.Context()))
}

func (s *Server) metrics(ctx context.Context) MetricsResponse {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	cpu, rss, _ := s.cpu.Last()
	hits, miss := s.nHits.Load(), s.nMiss.Load()
	m := MetricsResponse{
		Timestamp: time.Now().UTC(), UptimeS: time.Since(s.started).Seconds(),
		Coordinator: map[string]any{"goroutines": runtime.NumGoroutine(), "heapMB": float64(ms.HeapAlloc) / (1 << 20),
			"cpuPct": cpu, "rssMB": rss, "numCPU": runtime.NumCPU(), "persistQueue": len(s.persistCh),
			"persistDropped": s.dropped.Load()},
		Nodes: s.coord.Nodes(), Model: s.modelInfo(),
		Requests: map[string]int64{"predict": s.nPredict.Load(), "predictCacheHits": hits, "predictClusterCalls": miss,
			"patrullaje": s.nPatrol.Load(), "impacto": s.nImpact.Load(), "httpErrors": s.nErrors.Load(),
			"denied": s.nDenied.Load(), "rateLimited": s.nLimited.Load()},
		Latency: s.lat.summary(), Storage: s.store.Status(ctx),
		WebSocket: map[string]int64{"clients": s.hub.count.Load(), "connectionsTotal": s.hub.total.Load(),
			"messagesSent": s.hub.sent.Load(), "messagesDropped": s.hub.dropped.Load()},
	}
	if hits+miss > 0 {
		m.CacheHitPct = 100 * float64(hits) / float64(hits+miss)
	}
	return m
}

// metricsLoop guarda una foto de las métricas en MongoDB cada MetricsInterval.
func (s *Server) metricsLoop() {
	defer s.wg.Done()
	t := time.NewTicker(s.cfg.MetricsInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			m := s.metrics(ctx)
			cancel()
			if b, err := json.Marshal(m); err == nil {
				s.store.CacheSet(context.Background(), "tb2:metrics:latest", b, 2*s.cfg.MetricsInterval)
			}
			s.persist(storage.CollMetrics, m)
		}
	}
}

// LiveNode es la fila de un nodo en el panel en vivo.
type LiveNode struct {
	ID         string  `json:"id"`
	CPUPct     float64 `json:"cpuPct"`
	RSSMB      float64 `json:"rssMB"`
	HeapMB     float64 `json:"heapMB"`
	Goroutines int     `json:"goroutines"`
	NumCPU     int     `json:"numCPU"`
	Trained    bool    `json:"trained"`
	Version    int64   `json:"version"`
	Trees      int     `json:"trees"`
	Records    int64   `json:"trainRecords"`
	Training   bool    `json:"training"`
	Phase      string  `json:"phase"`
	TreesDone  int     `json:"treesDone"`
	TreesTotal int     `json:"treesTotal"`
	LastSeenS  float64 `json:"lastSeenS"`
	Ready      bool    `json:"ready"`
}

// liveLoop publica cada LiveInterval una foto liviana del clúster por WebSocket
// (solo si hay administradores conectados).
func (s *Server) liveLoop() {
	defer s.wg.Done()
	t := time.NewTicker(s.cfg.LiveInterval)
	defer t.Stop()
	var lastPredict int64
	last := time.Now()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			if s.hub.admins.Load() == 0 {
				lastPredict, last = s.nPredict.Load(), time.Now()
				continue
			}
			now := time.Now()
			n := s.nPredict.Load()
			rate := float64(n-lastPredict) / now.Sub(last).Seconds()
			lastPredict, last = n, now
			ready := map[string]bool{}
			for _, id := range s.modelInfo().NodesReady {
				ready[id] = true
			}
			var nodes []LiveNode
			for _, ni := range s.coord.Nodes() {
				st := ni.Stats
				nodes = append(nodes, LiveNode{ID: ni.ID, CPUPct: st.CPUPct, RSSMB: st.RSSMB, HeapMB: st.HeapMB,
					Goroutines: st.Goroutines, NumCPU: st.NumCPU, Trained: st.Trained, Version: st.Version, Trees: st.Trees,
					Records: st.Records, Training: st.Busy, Phase: st.Phase, TreesDone: st.TreesDone,
					TreesTotal: st.TreesTotal, LastSeenS: now.Sub(ni.LastSeen).Seconds(), Ready: ready[ni.ID]})
			}
			cpu, rss, _ := s.cpu.Last()
			hits, miss := s.nHits.Load(), s.nMiss.Load()
			hitPct := 0.0
			if hits+miss > 0 {
				hitPct = 100 * float64(hits) / float64(hits+miss)
			}
			lat := s.lat.summary()
			s.hub.publish(TopicMetrics, map[string]any{
				"nodes": nodes, "nodesExpected": s.cfg.ExpectedNodes,
				"coordinator":   map[string]any{"cpuPct": cpu, "rssMB": rss, "goroutines": runtime.NumGoroutine()},
				"predictPerSec": rate, "predictTotal": n, "cacheHitPct": hitPct,
				"latencyCluster": lat["predict_cluster"], "latencyCache": lat["predict_cache"],
				"httpErrors": s.nErrors.Load(), "denied": s.nDenied.Load(), "rateLimited": s.nLimited.Load(),
				"wsClients": s.hub.count.Load(), "model": s.modelInfo(),
			})
		}
	}
}

// handleLogs: GET /api/v1/admin/logs?limit=200&level=error&source=ml-node-1
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 200
	if v, err := strconv.Atoi(q.Get("limit")); err == nil && v > 0 && v <= 1000 {
		limit = v
	}
	entries := s.cfg.Logs.Last(limit, q.Get("level"), q.Get("source"))
	writeJSON(w, http.StatusOK, map[string]any{"count": len(entries), "entries": entries})
}

// ---------------------------------------------------------------- latencias

type latencies struct {
	mu   sync.Mutex
	data map[string][]float64
}

func newLatencies() *latencies { return &latencies{data: map[string][]float64{}} }

func (l *latencies) add(name string, ms float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	v := append(l.data[name], ms)
	if len(v) > 5000 {
		v = v[len(v)-5000:]
	}
	l.data[name] = v
}

func (l *latencies) summary() map[string]LatencySummary {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]LatencySummary{}
	for name, v := range l.data {
		if len(v) == 0 {
			continue
		}
		c := append([]float64(nil), v...)
		sort.Float64s(c)
		sum, under := 0.0, 0
		for _, x := range c {
			sum += x
			if x < 100 {
				under++
			}
		}
		pct := func(p float64) float64 { return c[int(p*float64(len(c)-1)+0.5)] }
		out[name] = LatencySummary{Count: len(c), AvgMs: sum / float64(len(c)), P50Ms: pct(0.50), P95Ms: pct(0.95),
			P99Ms: pct(0.99), MaxMs: c[len(c)-1], Under100: 100 * float64(under) / float64(len(c))}
	}
	return out
}
