// Package api es el coordinador HTTP: expone la API REST y el WebSocket,
// reparte el entrenamiento y las predicciones entre los nodos ML, aplica las
// políticas de seguridad (JWT, roles, CORS, cuotas) y persiste resultados.
package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jotabe12/cc65-tb2-grupo3/internal/auth"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/cluster"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/dataset"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/forest"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/logbuf"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/poblacion"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/protocol"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/storage"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/sysmetrics"
)

// SeedUser es un usuario que se crea (o actualiza) al iniciar el coordinador.
type SeedUser struct {
	Username, Password, Role string
}

type Config struct {
	CacheTTL        time.Duration
	TrainTimeout    time.Duration
	PredictTimeout  time.Duration
	MetricsInterval time.Duration // persistencia de métricas en MongoDB
	LiveInterval    time.Duration // métricas en vivo por WebSocket
	ExpectedNodes   int
	AutoTrain       bool // entrena el modelo por defecto cuando se conectan los nodos esperados
	OpenAPISpec     []byte
	Logf            func(format string, args ...any)

	Signer      *auth.Signer
	Users       []SeedUser
	CORSOrigins []string // orígenes permitidos (CORS y WebSocket)
	TrustProxy  bool     // tomar la IP del cliente de X-Real-IP (detrás de nginx)
	AnalystRPM  int      // cuota de peticiones por minuto para el rol analista (0 = sin límite)
	LoginPerMin int      // intentos fallidos de login por IP y minuto

	Logs       *logbuf.Ring
	Population *poblacion.Table
}

type Server struct {
	cfg   Config
	coord *cluster.Coordinator
	store storage.Store

	table       *dataset.Table
	cat         *dataset.Catalog
	hist        *dataset.History
	pop         []int64 // población 2026 por distrito (id del catálogo)
	loadStats   dataset.Stats
	started     time.Time
	origins     map[string]bool
	cellsMu     sync.Mutex
	cells       map[string][]dataset.Cell
	trainTotals map[int][dataset.NumClasses]int64

	trainMu sync.Mutex // un entrenamiento a la vez
	mu      sync.RWMutex
	model   *modelState
	jobs    []JobSummary
	// lastVersion: última versión guardada en MongoDB al arrancar; la primera
	// versión de este proceso es lastVersion+1 (evita repetir v1, v2... tras un
	// reinicio y que una clave de caché tb2:pred:vN apunte a otro modelo).
	lastVersion int64

	retraining sync.Map
	lat        *latencies
	nPredict   atomic.Int64
	nHits      atomic.Int64
	nMiss      atomic.Int64
	nPatrol    atomic.Int64
	nImpact    atomic.Int64
	nErrors    atomic.Int64
	nDenied    atomic.Int64
	nLimited   atomic.Int64

	hub       *hub
	logins    *ipLimiter
	quotas    *userQuota
	cpu       sysmetrics.Sampler
	persistCh chan persistJob
	dropped   atomic.Int64
	stop      chan struct{}
	stopOnce  sync.Once
	wg        sync.WaitGroup
}

// modelState describe el bosque vigente. Ready marca los nodos que tienen
// cargada esta versión; solo ellos participan en las predicciones.
type modelState struct {
	Version    int64
	TrainedAt  time.Time
	Config     forest.Config
	TotalTrees int
	FeatureSet dataset.FeatureSet
	Mode       string
	EvalYear   int
	Eval       *Evaluation
	Assign     map[string]protocol.TrainSpec
	Ready      map[string]bool
}

type persistJob struct {
	collection string
	doc        any
}

var (
	errNoReadyNodes    = errors.New("ningún nodo ML disponible con el modelo vigente")
	errUnknownDistrict = errors.New("distrito desconocido")
)

// New crea el servidor a partir del dataset ya cargado por el coordinador.
func New(cfg Config, coord *cluster.Coordinator, store storage.Store, t *dataset.Table, cat *dataset.Catalog) (*Server, error) {
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 10 * time.Minute
	}
	if cfg.TrainTimeout <= 0 {
		cfg.TrainTimeout = 5 * time.Minute
	}
	if cfg.PredictTimeout <= 0 {
		cfg.PredictTimeout = 3 * time.Second
	}
	if cfg.ExpectedNodes <= 0 {
		cfg.ExpectedNodes = 3
	}
	if cfg.LiveInterval <= 0 {
		cfg.LiveInterval = 2 * time.Second
	}
	if cfg.LoginPerMin <= 0 {
		cfg.LoginPerMin = 10
	}
	if cfg.Logs == nil {
		cfg.Logs = logbuf.New(1000)
	}
	if cfg.Signer == nil {
		return nil, errors.New("falta el firmador JWT (JWT_SECRET)")
	}
	s := &Server{
		cfg: cfg, coord: coord, store: store, table: t, cat: cat, hist: dataset.BuildHistory(t, cat),
		loadStats: t.Stats, started: time.Now(), origins: map[string]bool{}, cells: map[string][]dataset.Cell{},
		trainTotals: map[int][dataset.NumClasses]int64{},
		lat:         newLatencies(), hub: newHub(), logins: newIPLimiter(cfg.LoginPerMin, time.Minute),
		quotas: newUserQuota(cfg.AnalystRPM), persistCh: make(chan persistJob, 4096), stop: make(chan struct{}),
	}
	for _, o := range cfg.CORSOrigins {
		s.origins[o] = true
	}
	s.pop = make([]int64, len(cat.Districts))
	for i, u := range cat.Ubigeos {
		if p, ok := cfg.Population.Get(u, dataset.TestYear); ok {
			s.pop[i] = p
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, u := range cfg.Users {
		if err := s.seedUser(ctx, u); err != nil {
			return nil, err
		}
	}
	s.lastVersion = store.LastVersion(ctx)
	if s.lastVersion > 0 {
		cfg.Logf("historial en MongoDB: el próximo modelo será v%d", s.lastVersion+1)
	}
	cfg.Logs.OnAdd = s.onLog
	coord.OnRegister = s.onRegister
	coord.OnEvent = s.onNodeEvent
	s.wg.Add(3)
	go s.persistLoop()
	go s.liveLoop()
	go func() { defer s.wg.Done(); s.cpu.Run(time.Second, s.stop) }()
	if cfg.MetricsInterval > 0 {
		s.wg.Add(1)
		go s.metricsLoop()
	}
	return s, nil
}

// Close detiene las goroutines de fondo y vacía la cola de persistencia.
func (s *Server) Close() {
	s.stopOnce.Do(func() {
		close(s.stop)
		s.hub.closeAll()
		close(s.persistCh)
	})
	s.wg.Wait()
}

// persistLoop escribe en MongoDB de forma asíncrona: la latencia de la API no
// depende de la base de datos (un solo consumidor, cola con búfer).
func (s *Server) persistLoop() {
	defer s.wg.Done()
	for job := range s.persistCh {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := s.store.Insert(ctx, job.collection, job.doc); err != nil {
			s.cfg.Logf("persistencia %s: error %v", job.collection, err)
		}
		cancel()
	}
}

func (s *Server) persist(collection string, doc any) {
	defer func() { _ = recover() }() // la cola puede estar cerrada durante el apagado
	select {
	case s.persistCh <- persistJob{collection, doc}:
	default:
		s.dropped.Add(1) // cola llena: no se bloquea la petición
	}
}

// Handler registra las rutas (patrones con método de Go 1.22: un método no
// permitido responde 405 automáticamente) y la cadena de middlewares.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	all := []string{auth.RoleAdmin, auth.RoleAnalista}
	admin := []string{auth.RoleAdmin}

	// Públicos
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("GET /swagger.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		_, _ = w.Write(s.cfg.OpenAPISpec)
	})
	mux.HandleFunc("GET /swagger", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(swaggerHTML))
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/swagger", http.StatusFound)
	})
	// Autenticados (cualquier rol)
	mux.Handle("GET /api/v1/auth/me", s.authed(all, s.handleMe))
	mux.Handle("GET /api/v1/districts", s.authed(all, s.handleDistricts))
	mux.Handle("POST /api/v1/predict", s.authed(all, s.handlePredict))
	mux.Handle("GET /api/v1/patrullaje", s.authed(all, s.handlePatrol))
	mux.Handle("GET /api/v1/impacto", s.authed(all, s.handleImpact))
	mux.Handle("GET /api/v1/evaluacion", s.authed(all, s.handleEvaluation))
	mux.HandleFunc("GET /ws", s.handleWS) // autentica dentro (token por subprotocolo o query)
	// Solo administrador
	mux.Handle("POST /api/v1/train", s.authed(admin, s.handleTrain))
	mux.Handle("GET /api/v1/metrics", s.authed(admin, s.handleMetrics))
	mux.Handle("GET /api/v1/admin/logs", s.authed(admin, s.handleLogs))
	return s.middleware(s.cors(mux))
}

const swaggerHTML = `<!doctype html><html lang="es"><head><meta charset="utf-8"><title>API TB2 · Grupo 3</title>
<link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css"></head>
<body><div id="swagger-ui"></div>
<script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
<script>SwaggerUIBundle({url:'/swagger.yaml',dom_id:'#swagger-ui',displayRequestDuration:true,persistAuthorization:true})</script>
</body></html>`

// recorder fija X-Response-Time-Ms ANTES de enviar las cabeceras.
type recorder struct {
	http.ResponseWriter
	start  time.Time
	status int
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
		r.Header().Set("X-Response-Time-Ms", strconv.FormatFloat(msSince(r.start), 'f', 3, 64))
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(b)
}

// Hijack permite el handshake de WebSocket a través del recorder.
func (r *recorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijack no soportado")
	}
	if r.status == 0 {
		r.status = http.StatusSwitchingProtocols
	}
	return h.Hijack()
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &recorder{ResponseWriter: w, start: time.Now()}
		h := rec.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(rec, r)
		ms := msSince(rec.start)
		if rec.status >= 400 {
			s.nErrors.Add(1)
		}
		s.cfg.Logf("%s %s -> %d (%.3f ms)", r.Method, r.URL.Path, rec.status, ms)
	})
}

func msSince(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg, hint string) {
	writeJSON(w, status, ErrorResponse{Error: msg, Code: code, Hint: hint})
}

// ---------------------------------------------------------------- modelo

func (s *Server) current() *modelState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.model
}

func (s *Server) modelInfo() ModelInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	mi := ModelInfo{NodesReady: []string{}}
	if s.model == nil {
		return mi
	}
	st := s.model
	t := st.TrainedAt
	cfg := st.Config
	mi.Trained, mi.Version, mi.TrainedAt, mi.TotalTrees, mi.Config = true, st.Version, &t, st.TotalTrees, &cfg
	mi.FeatureSet, mi.Mode, mi.EvalYear = string(st.FeatureSet), st.Mode, st.EvalYear
	if st.Eval != nil {
		mi.Accuracy, mi.Top3 = st.Eval.Accuracy, st.Eval.Top3Accuracy
	}
	for id, ok := range st.Ready {
		if ok {
			mi.NodesReady = append(mi.NodesReady, id)
		}
	}
	sort.Strings(mi.NodesReady)
	return mi
}

// onRegister: si un nodo vuelve (p. ej. tras reiniciar su contenedor) y no
// tiene la versión vigente, se le reenvía su parte del entrenamiento. Si no hay
// modelo y ya están todos los nodos esperados, se entrena el modelo por defecto.
func (s *Server) onRegister(id string, stats protocol.NodeStats) {
	s.mu.Lock()
	st := s.model
	if st == nil {
		s.mu.Unlock()
		if s.cfg.AutoTrain && len(s.coord.NodeIDs()) >= s.cfg.ExpectedNodes {
			go s.autoTrain()
		}
		return
	}
	_, assigned := st.Assign[id]
	ready := assigned && stats.Trained && stats.Version == st.Version
	st.Ready[id] = ready
	s.mu.Unlock()
	if assigned && !ready {
		s.retrainNode(id, st)
	}
}

func (s *Server) retrainNode(id string, st *modelState) {
	if _, busy := s.retraining.LoadOrStore(id, true); busy {
		return
	}
	defer s.retraining.Delete(id)
	spec := st.Assign[id]
	s.cfg.Logf("reentrenando %s (versión %d, shard %d/%d, %d árboles)", id, st.Version, spec.Shard+1, spec.Shards, len(spec.TreeIDs))
	resp, err := s.coord.Call(id, protocol.Message{Type: protocol.TypeTrain, Train: &spec}, s.cfg.TrainTimeout)
	if err != nil || resp.Type != protocol.TypeTrainOK || resp.TrainResult == nil || resp.TrainResult.Version != st.Version {
		s.cfg.Logf("reentrenamiento de %s fallido: %v %s", id, err, resp.Error)
		return
	}
	s.mu.Lock()
	if s.model == st {
		st.Ready[id] = true
	}
	s.mu.Unlock()
	s.cfg.Logf("%s listo de nuevo con la versión %d", id, st.Version)
}

// clusterPredict reparte el lote de consultas a los nodos listos y combina
// sus probabilidades ponderando por número de árboles: el resultado es el
// promedio sobre TODOS los árboles del bosque distribuido.
func (s *Server) clusterPredict(st *modelState, qs []protocol.Query, timeout time.Duration) ([][dataset.NumClasses]float64, int, int, error) {
	s.mu.RLock()
	targets := make([]string, 0, len(st.Ready))
	for id, ok := range st.Ready {
		if ok {
			targets = append(targets, id)
		}
	}
	s.mu.RUnlock()
	if len(targets) == 0 {
		return nil, 0, 0, errNoReadyNodes
	}
	results, err := s.coord.Broadcast(targets, func(string) protocol.Message {
		return protocol.Message{Type: protocol.TypePredict, Queries: qs}
	}, timeout)
	if err != nil {
		return nil, 0, 0, errNoReadyNodes
	}
	sum := make([][dataset.NumClasses]float64, len(qs))
	used, trees := 0, 0
	for _, r := range results {
		if r.Err != nil {
			s.cfg.Logf("nodo %s no respondió: %v", r.NodeID, r.Err)
			continue
		}
		m := r.Msg
		if m.Type == protocol.TypeError {
			switch m.Code {
			case protocol.CodeUnknownDistrict:
				return nil, 0, 0, errUnknownDistrict
			case protocol.CodeNotTrained:
				s.markNotReady(st, r.NodeID)
			}
			s.cfg.Logf("nodo %s: %s (%s)", r.NodeID, m.Error, m.Code)
			continue
		}
		if m.Version != st.Version || len(m.Probs) != len(qs) || m.Trees == 0 {
			s.markNotReady(st, r.NodeID)
			continue
		}
		w := float64(m.Trees)
		for i, p := range m.Probs {
			for k := 0; k < dataset.NumClasses && k < len(p); k++ {
				sum[i][k] += w * p[k]
			}
		}
		used++
		trees += m.Trees
	}
	if used == 0 {
		return nil, 0, 0, errNoReadyNodes
	}
	for i := range sum {
		for k := range sum[i] {
			sum[i][k] /= float64(trees)
		}
	}
	return sum, used, trees, nil
}

func (s *Server) markNotReady(st *modelState, id string) {
	s.mu.Lock()
	if s.model == st {
		st.Ready[id] = false
	}
	s.mu.Unlock()
	go s.retrainNode(id, st)
}

// ---------------------------------------------------------------- handlers básicos

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ids := s.coord.NodeIDs()
	h := HealthResponse{
		Status: "ok", Time: time.Now().UTC(), UptimeS: time.Since(s.started).Seconds(),
		NodesExpected: s.cfg.ExpectedNodes, NodesConnected: len(ids), Nodes: ids,
		Model: s.modelInfo(), Storage: s.store.Status(r.Context()),
	}
	if len(ids) < s.cfg.ExpectedNodes {
		h.Status = "degradado"
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleDistricts(w http.ResponseWriter, r *http.Request) {
	out := make([]DistrictInfo, len(s.cat.Districts))
	for i, d := range s.cat.Districts {
		out[i] = DistrictInfo{District: d, Ubigeo: s.cat.Ubigeos[i], Zone: dataset.ZoneNames[s.cat.Zones[i]],
			Population: s.pop[i], Denuncias: s.cat.Volume[i], Street: s.cat.Street[i]}
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(out), "populationYear": dataset.TestYear,
		"populationSource": "INEI, población proyectada al 30 de junio (2018-2026)", "districts": out})
}

// cellsFor devuelve (y guarda) las celdas de evaluación de un conjunto y año.
func (s *Server) cellsFor(fs dataset.FeatureSet, year int) []dataset.Cell {
	key := fmt.Sprintf("%s|%d", fs, year)
	s.cellsMu.Lock()
	defer s.cellsMu.Unlock()
	if c, ok := s.cells[key]; ok {
		return c
	}
	c := dataset.AggregateYear(s.table, s.cat, fs, year)
	s.cells[key] = c
	return c
}

func (s *Server) totalsBefore(year int) [dataset.NumClasses]int64 {
	s.cellsMu.Lock()
	defer s.cellsMu.Unlock()
	if t, ok := s.trainTotals[year]; ok {
		return t
	}
	t := dataset.ClassTotals(s.table, func(y int) bool { return y < year })
	s.trainTotals[year] = t
	return t
}
