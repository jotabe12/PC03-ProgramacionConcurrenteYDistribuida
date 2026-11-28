// Package worker es el nodo ML: se conecta al coordinador por TCP, entrena su
// parte del bosque sobre su shard y responde predicciones.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jotabe12/cc65-tb2-grupo3/internal/dataset"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/forest"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/protocol"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/sysmetrics"
)

type Worker struct {
	ID          string
	Coordinator string
	DataPath    string
	Workers     int // goroutines para cargar y entrenar (0 = NumCPU)
	Logf        func(format string, args ...any)

	loadMu sync.Mutex
	table  *dataset.Table
	cat    *dataset.Catalog
	hist   *dataset.History

	trainMu sync.Mutex   // un entrenamiento a la vez
	mu      sync.RWMutex // protege el modelo: lecturas concurrentes, escritura exclusiva
	model   *forest.Model
	version int64
	shard   int
	records int64
	fs      dataset.FeatureSet

	cacheKey string          // matriz del último entrenamiento (solo la usa trainMu)
	cacheM   *dataset.Matrix // evita re-expandir si se repite el mismo shard

	emitMu sync.Mutex
	emit   func(protocol.Message) // envía mensajes espontáneos al coordinador (nil sin sesión)

	training   atomic.Bool
	phase      atomic.Value // string
	treesDone  atomic.Int64
	treesTotal atomic.Int64
	requests   atomic.Int64
	started    time.Time
	cpu        sysmetrics.Sampler
	stopCPU    chan struct{}
	cpuOnce    sync.Once
	stopOnce   sync.Once
}

func New(id, coordinator, dataPath string, workers int) *Worker {
	w := &Worker{ID: id, Coordinator: coordinator, DataPath: dataPath, Workers: workers, Logf: log.Printf,
		started: time.Now(), stopCPU: make(chan struct{})}
	w.phase.Store("inactivo")
	return w
}

// logf escribe en el log local y además envía la línea al coordinador, que la
// guarda en su buffer de logs y la muestra en el panel de administración.
func (w *Worker) logf(level, format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	w.Logf("[%s] %s", w.ID, text)
	w.send(protocol.Message{Type: protocol.TypeLog, NodeID: w.ID,
		Log: &protocol.LogLine{Time: time.Now().UTC(), Level: level, Text: text}})
}

func (w *Worker) send(m protocol.Message) {
	w.emitMu.Lock()
	f := w.emit
	w.emitMu.Unlock()
	if f != nil {
		f(m)
	}
}

// RunForever mantiene la conexión con el coordinador y reintenta si se cae.
// El modelo queda en memoria del proceso, así que una reconexión no lo pierde.
func (w *Worker) RunForever(ctx context.Context) {
	w.cpuOnce.Do(func() { go w.cpu.Run(time.Second, w.stopCPU) })
	defer w.stopOnce.Do(func() { close(w.stopCPU) })
	backoff := time.Second
	for ctx.Err() == nil {
		err := w.Run(ctx)
		if ctx.Err() != nil {
			return
		}
		w.Logf("[%s] conexión con el coordinador perdida (%v); reintento en %s", w.ID, err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

// Run atiende una sesión TCP. Cada mensaje se procesa en su propia goroutine;
// las escrituras al socket se serializan con un mutex.
func (w *Worker) Run(ctx context.Context) error {
	conn, err := net.DialTimeout("tcp", w.Coordinator, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	done := make(chan struct{})
	defer close(done)
	go func() { // cierra el socket si se cancela el contexto (sin fugar goroutines)
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()

	var encMu sync.Mutex
	enc := json.NewEncoder(conn)
	send := func(m protocol.Message) {
		encMu.Lock()
		defer encMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := enc.Encode(m); err != nil {
			w.Logf("[%s] error enviando %s: %v", w.ID, m.Type, err)
		}
	}
	send(protocol.Message{Type: protocol.TypeRegister, NodeID: w.ID, Stats: w.stats()})
	w.emitMu.Lock()
	w.emit = send
	w.emitMu.Unlock()
	defer func() {
		w.emitMu.Lock()
		w.emit = nil
		w.emitMu.Unlock()
	}()
	w.logf("info", "registrado en el coordinador %s (%d núcleos)", w.Coordinator, runtime.NumCPU())

	dec := json.NewDecoder(conn)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		var msg protocol.Message
		if err := dec.Decode(&msg); err != nil {
			return err
		}
		wg.Add(1)
		go func(m protocol.Message) {
			defer wg.Done()
			resp := w.Handle(m)
			resp.ID = m.ID
			resp.NodeID = w.ID
			send(resp)
		}(msg)
	}
}

// Handle procesa un mensaje y devuelve la respuesta (también usado en pruebas).
func (w *Worker) Handle(m protocol.Message) protocol.Message {
	w.requests.Add(1)
	switch m.Type {
	case protocol.TypePing:
		return protocol.Message{Type: protocol.TypePong, Stats: w.stats()}
	case protocol.TypeTrain:
		if m.Train == nil {
			return errMsg(protocol.CodeBadRequest, "TRAIN sin especificación")
		}
		res, err := w.Train(*m.Train)
		if err != nil {
			w.logf("error", "entrenamiento fallido: %v", err)
			return errMsg(protocol.CodeInternal, err.Error())
		}
		return protocol.Message{Type: protocol.TypeTrainOK, TrainResult: res}
	case protocol.TypePredict:
		return w.predict(m.Queries)
	default:
		return errMsg(protocol.CodeBadRequest, "tipo de mensaje desconocido: "+m.Type)
	}
}

func errMsg(code, text string) protocol.Message {
	return protocol.Message{Type: protocol.TypeError, Code: code, Error: text}
}

func (w *Worker) load(path string) (*dataset.Table, *dataset.Catalog, *dataset.History, float64, error) {
	w.loadMu.Lock()
	defer w.loadMu.Unlock()
	if w.table != nil && (path == "" || path == w.DataPath) {
		return w.table, w.cat, w.hist, 0, nil
	}
	if path == "" {
		path = w.DataPath
	}
	start := time.Now()
	t, err := dataset.Load(path, w.Workers)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	c, err := dataset.BuildCatalog(t)
	if err != nil {
		return nil, nil, nil, 0, err
	}
	h := dataset.BuildHistory(t, c)
	w.mu.Lock()
	w.table, w.cat, w.hist, w.DataPath = t, c, h, path
	w.mu.Unlock()
	return t, c, h, float64(time.Since(start).Microseconds()) / 1000, nil
}

// Train carga el CSV (una vez por proceso), arma su shard de entrenamiento
// (años TrainFrom..TrainTo) y entrena sus árboles en paralelo. El modelo nuevo
// se construye fuera del candado y se publica con un único Lock (las
// predicciones en curso nunca ven un modelo a medio construir).
func (w *Worker) Train(spec protocol.TrainSpec) (*protocol.TrainResult, error) {
	if spec.Shards < 1 || spec.Shard < 0 || spec.Shard >= spec.Shards || len(spec.TreeIDs) == 0 {
		return nil, fmt.Errorf("especificación de entrenamiento inválida: shard %d/%d, %d árboles", spec.Shard, spec.Shards, len(spec.TreeIDs))
	}
	fs, err := dataset.ParseFeatureSet(spec.FeatureSet)
	if err != nil {
		return nil, err
	}
	if spec.TrainFrom == 0 {
		spec.TrainFrom = dataset.BaseYear
	}
	if spec.TrainTo == 0 {
		spec.TrainTo = dataset.TestYear - 1
	}
	if spec.TrainFrom > spec.TrainTo {
		return nil, fmt.Errorf("período de entrenamiento inválido: %d-%d", spec.TrainFrom, spec.TrainTo)
	}
	w.trainMu.Lock()
	defer w.trainMu.Unlock()
	w.training.Store(true)
	defer func() {
		w.training.Store(false)
		w.phase.Store("inactivo")
	}()
	start := time.Now()
	total := len(spec.TreeIDs)
	w.treesDone.Store(0)
	w.treesTotal.Store(int64(total))
	progress := func(phase string, done int, records int64) {
		w.phase.Store(phase)
		w.send(protocol.Message{Type: protocol.TypeProgress, NodeID: w.ID, Progress: &protocol.Progress{
			Version: spec.Version, Phase: phase, Done: done, Total: total, Records: records,
			ElapsedMs: float64(time.Since(start).Microseconds()) / 1000}})
	}
	w.logf("info", "inicia entrenamiento v%d: shard %d/%d, %d árboles, variables %s, años %d-%d",
		spec.Version, spec.Shard+1, spec.Shards, total, fs, spec.TrainFrom, spec.TrainTo)

	progress("cargando", 0, 0)
	t, c, h, loadMs, err := w.load(spec.DataPath)
	if err != nil {
		return nil, err
	}
	progress("expandiendo", 0, 0)
	t1 := time.Now()
	key := fmt.Sprintf("%s|%d/%d|%d-%d", fs, spec.Shard, spec.Shards, spec.TrainFrom, spec.TrainTo)
	m, cached := w.cacheM, true
	if w.cacheKey != key || m == nil {
		cached = false
		m = dataset.Expand(t, c, h, fs, spec.Shard, spec.Shards, func(y int) bool { return y >= spec.TrainFrom && y <= spec.TrainTo })
		w.cacheKey, w.cacheM = key, m
	}
	expandMs := float64(time.Since(t1).Microseconds()) / 1000
	workers := spec.Workers
	if workers <= 0 {
		workers = w.Workers
	}
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	records := int64(m.Len())
	progress("entrenando", 0, records)
	var lastSent atomic.Int64
	t2 := time.Now()
	model := forest.Train(m, dataset.SchemaFor(fs, c), spec.TreeIDs, spec.Config, workers, func(done, total int) {
		w.treesDone.Store(int64(done))
		// a lo más ~10 avisos por segundo (y siempre el último)
		now := time.Now().UnixMilli()
		prev := lastSent.Load()
		if done == total || (now-prev >= 100 && lastSent.CompareAndSwap(prev, now)) {
			progress("entrenando", done, records)
		}
	})
	trainMs := float64(time.Since(t2).Microseconds()) / 1000
	nodes, sig := model.Signature()

	w.mu.Lock()
	w.model, w.version, w.shard, w.records, w.fs = model, spec.Version, spec.Shard, records, fs
	w.mu.Unlock()
	progress("listo", total, records)

	res := &protocol.TrainResult{
		Version: spec.Version, Shard: spec.Shard, Shards: spec.Shards, Trees: len(model.Trees), TreeIDs: spec.TreeIDs,
		TrainRecords: records, ClassCounts: m.Count, Workers: workers, LoadMs: loadMs, ExpandMs: expandMs,
		TrainMs: trainMs, TotalMs: float64(time.Since(start).Microseconds()) / 1000, TreeNodes: nodes,
		Importance: model.Importance, Signature: sig, FeatureSet: string(fs), CachedMatrix: cached,
	}
	w.logf("info", "entrenado v%d: shard %d/%d, %d denuncias, %d árboles con %d goroutines en %.1f ms",
		spec.Version, spec.Shard+1, spec.Shards, records, len(model.Trees), workers, trainMs)
	return res, nil
}

var errUnknown = errors.New("distrito desconocido")

func (w *Worker) predict(qs []protocol.Query) protocol.Message {
	w.mu.RLock()
	model, version, cat, hist, fs := w.model, w.version, w.cat, w.hist, w.fs
	w.mu.RUnlock()
	if model == nil || cat == nil {
		// Sin modelo NO se inventan probabilidades: el coordinador decide (409 / reentrenar).
		return errMsg(protocol.CodeNotTrained, "el nodo no tiene un modelo entrenado")
	}
	out := make([][]float64, len(qs))
	for i, q := range qs {
		if q.Franja < 0 || q.Franja >= dataset.NumFranjas || q.Mes < 1 || q.Mes > 12 ||
			q.Anio < dataset.BaseYear || q.Anio > dataset.BaseYear+dataset.MaxYearIndex {
			return errMsg(protocol.CodeBadRequest, fmt.Sprintf("consulta %d fuera de rango", i))
		}
		id, ok := cat.Lookup(q.District)
		if !ok {
			return errMsg(protocol.CodeUnknownDistrict, fmt.Sprintf("%v: %s", errUnknown, q.District))
		}
		p := model.Predict(dataset.Vector(fs, cat, hist, id, q.Franja, q.Mes, q.Anio))
		out[i] = p[:]
	}
	return protocol.Message{Type: protocol.TypePredictOK, Probs: out, Trees: len(model.Trees), Version: version}
}

func (w *Worker) stats() *protocol.NodeStats {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	cpu, rss, _ := w.cpu.Last()
	w.mu.RLock()
	defer w.mu.RUnlock()
	trees := 0
	if w.model != nil {
		trees = len(w.model.Trees)
	}
	phase, _ := w.phase.Load().(string)
	return &protocol.NodeStats{
		Trained: w.model != nil, Version: w.version, Trees: trees, Shard: w.shard, Records: w.records,
		Goroutines: runtime.NumGoroutine(), HeapMB: float64(ms.HeapAlloc) / (1 << 20), NumCPU: runtime.NumCPU(),
		UptimeS: time.Since(w.started).Seconds(), Requests: w.requests.Load(), Busy: w.training.Load(),
		CPUPct: cpu, RSSMB: rss, FeatureSet: string(w.fs), Phase: phase,
		TreesDone: int(w.treesDone.Load()), TreesTotal: int(w.treesTotal.Load()),
	}
}
