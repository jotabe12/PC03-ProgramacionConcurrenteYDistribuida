package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/jotabe12/cc65-tb2-grupo3/internal/cluster"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/dataset"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/forest"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/protocol"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/storage"
)

// Modos de entrenamiento (partición temporal, nunca aleatoria).
const (
	ModeFinal      = "final"      // entrena 2018-2025, evalúa 2026 (como la PC3/PC4)
	ModeValidacion = "validacion" // entrena 2018-2024, evalúa 2025 (para elegir variables e hiperparámetros)
)

// trainPlan es un entrenamiento ya validado.
type trainPlan struct {
	cfg       forest.Config
	trees     int
	workers   int
	nodes     int
	fs        dataset.FeatureSet
	mode      string
	trainFrom int
	trainTo   int
	evalYear  int
	requestBy string
}

func (s *Server) defaultPlan() trainPlan {
	return trainPlan{cfg: forest.DefaultConfig(), trees: 32, fs: dataset.DefaultFeatureSet, mode: ModeFinal,
		trainFrom: dataset.BaseYear, trainTo: dataset.TestYear - 1, evalYear: dataset.TestYear}
}

func (s *Server) handleTrain(w http.ResponseWriter, r *http.Request) {
	var req TrainRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "JSON inválido: "+err.Error(), `{"trees":32,"workers":4,"nodes":3,"featureSet":"tb2"}`)
		return
	}
	plan := s.defaultPlan()
	plan.requestBy = claimsOf(r).Sub
	if msg := s.applyTrain(&req, &plan); msg != "" {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", msg, "")
		return
	}
	if !s.trainMu.TryLock() {
		writeErr(w, http.StatusConflict, "TRAINING_IN_PROGRESS", "ya hay un entrenamiento en curso", "espere a que termine")
		return
	}
	defer s.trainMu.Unlock()
	job, status, code, err := s.train(plan)
	if err != nil {
		writeErr(w, status, code, err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) applyTrain(req *TrainRequest, p *trainPlan) string {
	if req.FeatureSet != nil {
		fs, err := dataset.ParseFeatureSet(*req.FeatureSet)
		if err != nil {
			return err.Error()
		}
		p.fs = fs
	}
	if req.Mode != nil {
		switch *req.Mode {
		case ModeFinal, "":
		case ModeValidacion:
			p.mode, p.trainTo, p.evalYear = ModeValidacion, dataset.ValYear-1, dataset.ValYear
		default:
			return "mode debe ser final o validacion"
		}
	}
	if req.Trees != nil {
		if *req.Trees < 1 || *req.Trees > 512 {
			return "trees debe estar entre 1 y 512"
		}
		p.trees = *req.Trees
	}
	if req.MaxDepth != nil {
		if *req.MaxDepth < 1 || *req.MaxDepth > 24 {
			return "maxDepth debe estar entre 1 y 24"
		}
		p.cfg.MaxDepth = *req.MaxDepth
	}
	if req.MinLeaf != nil {
		if *req.MinLeaf < 1 || *req.MinLeaf > 100000 {
			return "minLeaf debe estar entre 1 y 100000"
		}
		p.cfg.MinLeaf = *req.MinLeaf
	}
	nf := dataset.SchemaFor(p.fs, s.cat).Len()
	if req.Mtry != nil {
		if *req.Mtry < 1 || *req.Mtry > nf {
			return fmt.Sprintf("mtry debe estar entre 1 y %d (variables del conjunto %s)", nf, p.fs)
		}
		p.cfg.Mtry = *req.Mtry
	}
	if p.cfg.Mtry > nf {
		p.cfg.Mtry = nf
	}
	if req.SampleFrac != nil {
		if *req.SampleFrac <= 0 || *req.SampleFrac > 1 {
			return "sampleFrac debe estar en (0, 1]"
		}
		p.cfg.SampleFrac = *req.SampleFrac
	}
	if req.Workers != nil {
		if *req.Workers < 0 || *req.Workers > 64 {
			return "workers debe estar entre 0 y 64 (0 = todos los núcleos del nodo)"
		}
		p.workers = *req.Workers
	}
	if req.Nodes != nil {
		if *req.Nodes < 1 || *req.Nodes > 16 {
			return "nodes debe estar entre 1 y 16"
		}
		p.nodes = *req.Nodes
	}
	if req.ClassWeight != nil {
		cw, err := forest.ParseClassWeight(*req.ClassWeight)
		if err != nil {
			return err.Error()
		}
		p.cfg.ClassWeight = cw
	}
	return ""
}

func (s *Server) autoTrain() {
	if !s.trainMu.TryLock() {
		return
	}
	defer s.trainMu.Unlock()
	if s.current() != nil {
		return
	}
	p := s.defaultPlan()
	p.requestBy = "sistema (AUTO_TRAIN)"
	s.cfg.Logf("AUTO_TRAIN: entrenando el modelo por defecto (%s, %d árboles)", p.fs, p.trees)
	if _, _, _, err := s.train(p); err != nil {
		s.cfg.Logf("AUTO_TRAIN fallido: %v", err)
	}
}

// train reparte el bosque entre los nodos (fan-out), espera a todos (fan-in),
// evalúa y publica el modelo nuevo. Debe llamarse con trainMu tomado.
func (s *Server) train(p trainPlan) (*TrainingJob, int, string, error) {
	ids := s.coord.NodeIDs()
	if len(ids) == 0 {
		return nil, http.StatusServiceUnavailable, "NO_NODES", cluster.ErrNoNodes
	}
	if p.nodes > 0 {
		if p.nodes > len(ids) {
			return nil, http.StatusConflict, "NOT_ENOUGH_NODES", fmt.Errorf("se pidieron %d nodos y hay %d conectados", p.nodes, len(ids))
		}
		ids = ids[:p.nodes]
	}
	trees := p.trees
	if trees < len(ids) {
		trees = len(ids)
	}
	prev := s.current()
	version := int64(1)
	if prev != nil {
		version = prev.Version + 1
	}
	// Reparto de árboles: bloques contiguos de ids globales (32 -> 11, 11, 10) y
	// shard i de N (N = nodos usados): cada nodo entrena con ~1/N de las denuncias.
	specs := map[string]protocol.TrainSpec{}
	next := 0
	for i, id := range ids {
		n := trees / len(ids)
		if i < trees%len(ids) {
			n++
		}
		tids := make([]int, n)
		for j := range tids {
			tids[j] = next
			next++
		}
		specs[id] = protocol.TrainSpec{Version: version, Shard: i, Shards: len(ids), TreeIDs: tids, Config: p.cfg,
			Workers: p.workers, FeatureSet: string(p.fs), TrainFrom: p.trainFrom, TrainTo: p.trainTo}
	}
	sch := dataset.SchemaFor(p.fs, s.cat)
	s.hub.publish(TopicTraining, map[string]any{"event": "inicio", "version": version, "nodes": ids, "trees": trees,
		"featureSet": p.fs, "mode": p.mode, "workers": p.workers, "requestedBy": p.requestBy})
	s.cfg.Logf("entrenamiento v%d: %d nodos, %d árboles, variables %s, modo %s (%d-%d -> %d), pedido por %s",
		version, len(ids), trees, p.fs, p.mode, p.trainFrom, p.trainTo, p.evalYear, p.requestBy)
	start := time.Now()
	results, err := s.coord.Broadcast(ids, func(id string) protocol.Message {
		sp := specs[id]
		return protocol.Message{Type: protocol.TypeTrain, Train: &sp}
	}, s.cfg.TrainTimeout)
	if err != nil {
		return nil, http.StatusServiceUnavailable, "NO_NODES", err
	}
	job := &TrainingJob{
		JobID: fmt.Sprintf("job-%d-v%d", time.Now().Unix(), version), Version: version, CreatedAt: time.Now().UTC(),
		RequestedBy: p.requestBy, Mode: p.mode, FeatureSet: string(p.fs), Features: sch.Names, TrainFrom: p.trainFrom,
		TrainTo: p.trainTo, DurationMs: msSince(start), NodeCount: len(ids), Workers: p.workers, TotalTrees: trees,
		Config: p.cfg, Dataset: s.loadStats,
	}
	imp := make([]float64, sch.Len())
	for _, res := range results {
		if res.Err != nil || res.Msg.Type != protocol.TypeTrainOK || res.Msg.TrainResult == nil {
			msg := res.Msg.Error
			if res.Err != nil {
				msg = res.Err.Error()
			}
			s.hub.publish(TopicTraining, map[string]any{"event": "error", "version": version, "node": res.NodeID, "error": msg})
			return nil, http.StatusBadGateway, "NODE_TRAIN_FAILED", fmt.Errorf("el nodo %s no pudo entrenar: %s", res.NodeID, msg)
		}
		tr := *res.Msg.TrainResult
		job.Nodes = append(job.Nodes, NodeTrain{NodeID: res.NodeID, RoundTripMs: res.Ms, Result: tr})
		job.TrainRecords += tr.TrainRecords
		job.MaxTrainMs = max(job.MaxTrainMs, tr.TrainMs)
		job.MaxExpandMs = max(job.MaxExpandMs, tr.ExpandMs)
		for f := range imp {
			if f < len(tr.Importance) {
				imp[f] += tr.Importance[f]
			}
		}
	}
	st := &modelState{Version: version, TrainedAt: time.Now().UTC(), Config: p.cfg, TotalTrees: trees, FeatureSet: p.fs,
		Mode: p.mode, EvalYear: p.evalYear, Assign: specs, Ready: map[string]bool{}}
	for _, id := range ids {
		st.Ready[id] = true
	}
	ev, err := s.evaluate(st, sch, imp)
	if err != nil {
		job.EvalError = err.Error()
	} else {
		job.Evaluation = ev
		st.Eval = ev
	}
	job.Status = "completed"
	s.mu.Lock()
	s.model = st
	s.jobs = append(s.jobs, summarize(job))
	if len(s.jobs) > 200 {
		s.jobs = s.jobs[len(s.jobs)-200:]
	}
	s.mu.Unlock()
	if b, err := json.Marshal(s.modelInfo()); err == nil {
		s.store.CacheSet(context.Background(), "tb2:model:current", b, 0)
	}
	s.persist(storage.CollTrainingJobs, job)
	fin := map[string]any{"event": "fin", "version": version, "durationMs": job.DurationMs, "maxTrainMs": job.MaxTrainMs,
		"nodes": len(ids), "trees": trees, "featureSet": p.fs, "mode": p.mode}
	if ev != nil {
		fin["accuracy"], fin["top3Accuracy"], fin["macroF1"] = ev.Accuracy, ev.Top3Accuracy, ev.MacroF1
	}
	s.hub.publish(TopicTraining, fin)
	s.cfg.Logf("modelo v%d listo en %.1f ms (exactitud %.4f)", version, job.DurationMs, st.accuracy())
	return job, http.StatusOK, "", nil
}

func (st *modelState) accuracy() float64 {
	if st.Eval == nil {
		return 0
	}
	return st.Eval.Accuracy
}

func summarize(j *TrainingJob) JobSummary {
	js := JobSummary{JobID: j.JobID, Version: j.Version, CreatedAt: j.CreatedAt, Mode: j.Mode, FeatureSet: j.FeatureSet,
		ClassWeight: j.Config.ClassWeight, NodeCount: j.NodeCount, Workers: j.Workers, TotalTrees: j.TotalTrees,
		Config: j.Config, DurationMs: j.DurationMs, MaxTrainMs: j.MaxTrainMs}
	if e := j.Evaluation; e != nil {
		js.EvalYear, js.Accuracy, js.Top3, js.MacroF1, js.Baseline, js.Persistence =
			e.EvalYear, e.Accuracy, e.Top3Accuracy, e.MacroF1, e.Baseline, e.Persistence
	}
	return js
}

// evaluate mide el bosque distribuido en el año de evaluación del modo. Las
// denuncias se agrupan en vectores distintos y se consultan en un solo lote.
func (s *Server) evaluate(st *modelState, sch dataset.Schema, imp []float64) (*Evaluation, error) {
	start := time.Now()
	cells := s.cellsFor(st.FeatureSet, st.EvalYear)
	qs := make([]protocol.Query, len(cells))
	for i, c := range cells {
		qs[i] = protocol.Query{District: s.cat.Districts[c.District], Franja: c.Franja, Mes: c.Month, Anio: c.Year}
	}
	probs, used, trees, err := s.clusterPredict(st, qs, 60*time.Second)
	if err != nil {
		return nil, err
	}
	ev := &Evaluation{EvalYear: st.EvalYear, FeatureSet: string(st.FeatureSet), Queries: len(qs), NodesUsed: used, TreesUsed: trees}
	ev.Classes = dataset.ClassNames[:]
	trainTotals := s.totalsBefore(st.EvalYear)
	base := argmax(nil, trainTotals[:])
	ev.BaselineClass = dataset.ClassNames[base]
	var top3, persist int64
	for i, c := range cells {
		order := rank(probs[i][:])
		mix, _ := s.hist.Recent(c.District, dataset.MonthIndex(c.Year, c.Month))
		pers := 0
		for k := 1; k < dataset.NumClasses; k++ {
			if mix[k] > mix[pers] {
				pers = k
			}
		}
		for k := 0; k < dataset.NumClasses; k++ {
			ev.Confusion[k][order[0]] += c.Counts[k]
			ev.TestRecords += c.Counts[k]
			if k == order[0] || k == order[1] || k == order[2] {
				top3 += c.Counts[k]
			}
			if k == pers {
				persist += c.Counts[k]
			}
		}
	}
	var correct, baseHits int64
	for k := 0; k < dataset.NumClasses; k++ {
		var real, predicted int64
		for j := 0; j < dataset.NumClasses; j++ {
			real += ev.Confusion[k][j]
			predicted += ev.Confusion[j][k]
		}
		correct += ev.Confusion[k][k]
		if k == base {
			baseHits = real
		}
		cm := ClassMetric{Class: dataset.ClassNames[k], Real: real, Predicted: predicted}
		if predicted > 0 {
			cm.Precision = float64(ev.Confusion[k][k]) / float64(predicted)
		}
		if real > 0 {
			cm.Recall = float64(ev.Confusion[k][k]) / float64(real)
		}
		if cm.Precision+cm.Recall > 0 {
			cm.F1 = 2 * cm.Precision * cm.Recall / (cm.Precision + cm.Recall)
		}
		ev.MacroRecall += cm.Recall / dataset.NumClasses
		ev.MacroF1 += cm.F1 / dataset.NumClasses
		if ev.TestRecords > 0 {
			ev.WeightedF1 += cm.F1 * float64(real) / float64(ev.TestRecords)
		}
		ev.PerClass = append(ev.PerClass, cm)
	}
	if ev.TestRecords > 0 {
		n := float64(ev.TestRecords)
		ev.Accuracy, ev.Baseline = float64(correct)/n, float64(baseHits)/n
		ev.Top3Accuracy, ev.Persistence = float64(top3)/n, float64(persist)/n
	}
	total := 0.0
	for _, v := range imp {
		total += v
	}
	for f, v := range imp {
		share := 0.0
		if total > 0 {
			share = v / total
		}
		ev.Importance = append(ev.Importance, FeatureImportance{Feature: sch.Names[f], Importance: share})
	}
	ev.EvalMs = msSince(start)
	return ev, nil
}

// rank devuelve las clases ordenadas de mayor a menor probabilidad (estable).
func rank(p []float64) []int {
	order := make([]int, len(p))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		switch {
		case p[a] > p[b]:
			return -1
		case p[a] < p[b]:
			return 1
		}
		return 0
	})
	return order
}

func argmax(p []float64, c []int64) int {
	best := 0
	if p != nil {
		for i := 1; i < len(p); i++ {
			if p[i] > p[best] {
				best = i
			}
		}
		return best
	}
	for i := 1; i < len(c); i++ {
		if c[i] > c[best] {
			best = i
		}
	}
	return best
}

func (s *Server) handleEvaluation(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	var cur *Evaluation
	if s.model != nil {
		cur = s.model.Eval
	}
	hist := append([]JobSummary(nil), s.jobs...)
	s.mu.RUnlock()
	slices.Reverse(hist) // más reciente primero
	writeJSON(w, http.StatusOK, EvaluationResponse{Model: s.modelInfo(), Current: cur, History: hist})
}
