package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jotabe12/cc65-tb2-grupo3/internal/dataset"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/protocol"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/storage"
)

const franjaNote = "Distribución horaria orientativa: el CSV no registra la hora del hecho. Se calcula con el supuesto " +
	"de la PC3 (Tabla 11: P(franja | modalidad)) ponderado por las probabilidades del bosque; no es una predicción del modelo."

var monthNames = [12]string{"ene", "feb", "mar", "abr", "may", "jun", "jul", "ago", "sep", "oct", "nov", "dic"}

func monthLabel(idx int) string {
	return fmt.Sprintf("%s-%d", monthNames[idx%12], dataset.BaseYear+idx/12)
}

// franjaSupuesta: P(franja) = Σ_c P(c | x) · P(franja | c), con P(franja | c) del supuesto de la PC3.
func franjaSupuesta(p *[dataset.NumClasses]float64, onlyStreet bool) []FranjaProb {
	var out [dataset.NumFranjas]float64
	tot := 0.0
	for c := 0; c < dataset.NumClasses; c++ {
		if onlyStreet && !dataset.StreetCrime[c] {
			continue
		}
		prior := dataset.PriorFranja(c)
		for f := 0; f < dataset.NumFranjas; f++ {
			out[f] += p[c] * prior[f]
			tot += p[c] * prior[f]
		}
	}
	res := make([]FranjaProb, dataset.NumFranjas)
	for f := range res {
		v := 0.0
		if tot > 0 {
			v = out[f] / tot
		}
		res[f] = FranjaProb{Franja: dataset.FranjaNames[f], Probability: v}
	}
	return res
}

func topFranja(fp []FranjaProb) string {
	best := 0
	for i := range fp {
		if fp[i].Probability > fp[best].Probability {
			best = i
		}
	}
	return fp[best].Franja
}

func topStreet(p *[dataset.NumClasses]float64) int {
	best := -1
	for k := 0; k < dataset.NumClasses; k++ {
		if dataset.StreetCrime[k] && (best < 0 || p[k] > p[best]) {
			best = k
		}
	}
	return best
}

func parseMonthYear(r *http.Request) (int, int, string) {
	q := r.URL.Query()
	mes, err1 := strconv.Atoi(q.Get("mes"))
	anio, err2 := strconv.Atoi(q.Get("anio"))
	if err1 != nil || err2 != nil || mes < 1 || mes > 12 || anio < dataset.BaseYear || anio > dataset.BaseYear+dataset.MaxYearIndex {
		return 0, 0, fmt.Sprintf("parámetros mes (1-12) y anio (%d-%d) obligatorios", dataset.BaseYear, dataset.BaseYear+dataset.MaxYearIndex)
	}
	return mes, anio, ""
}

// ---------------------------------------------------------------- predicción

func (s *Server) handlePredict(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req PredictRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "JSON inválido: "+err.Error(), `{"district":"SAN JUAN DE LURIGANCHO","mes":10,"anio":2026}`)
		return
	}
	if req.District == nil || req.Mes == nil || req.Anio == nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "district, mes y anio son obligatorios", `{"district":"SAN JUAN DE LURIGANCHO","mes":10,"anio":2026}`)
		return
	}
	mes, anio := *req.Mes, *req.Anio
	if mes < 1 || mes > 12 {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "mes debe estar entre 1 y 12", "")
		return
	}
	if anio < dataset.BaseYear || anio > dataset.BaseYear+dataset.MaxYearIndex {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", fmt.Sprintf("anio debe estar entre %d y %d", dataset.BaseYear, dataset.BaseYear+dataset.MaxYearIndex), "")
		return
	}
	id, ok := s.cat.Lookup(*req.District)
	if !ok {
		writeErr(w, http.StatusNotFound, "UNKNOWN_DISTRICT", "distrito no encontrado en Lima Metropolitana y Callao: "+*req.District, "consulte GET /api/v1/districts")
		return
	}
	district := s.cat.Districts[id]
	st := s.current()
	if st == nil {
		writeErr(w, http.StatusConflict, "MODEL_NOT_TRAINED", "el modelo aún no ha sido entrenado", "ejecute POST /api/v1/train (rol admin)")
		return
	}
	franja := 0
	if st.FeatureSet == dataset.FSPC4 {
		if req.Franja == nil || *req.Franja < 0 || *req.Franja >= dataset.NumFranjas {
			writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "el modelo vigente (pc4) usa la franja imputada: indique franja 0-3", "")
			return
		}
		franja = *req.Franja
	}
	user := claimsOf(r).Sub
	s.nPredict.Add(1)
	key := fmt.Sprintf("tb2:pred:v%d:%s:%d:%d:%d", st.Version, district, franja, mes, anio)
	if b, ok := s.store.CacheGet(r.Context(), key); ok {
		var resp PredictResponse
		if json.Unmarshal(b, &resp) == nil {
			s.nHits.Add(1)
			resp.FromCache = true
			resp.LatencyMs = msSince(start)
			s.lat.add("predict_cache", resp.LatencyMs)
			s.afterPredict(&resp, user)
			writeJSON(w, http.StatusOK, resp)
			return
		}
	}
	s.nMiss.Add(1)
	t0 := time.Now()
	probs, used, trees, err := s.clusterPredict(st, []protocol.Query{{District: district, Franja: franja, Mes: mes, Anio: anio}}, s.cfg.PredictTimeout)
	clusterMs := msSince(t0)
	if err != nil {
		code, status := "CLUSTER_UNAVAILABLE", http.StatusServiceUnavailable
		if errors.Is(err, errUnknownDistrict) {
			code, status = "UNKNOWN_DISTRICT", http.StatusNotFound
		}
		writeErr(w, status, code, err.Error(), "")
		return
	}
	p := probs[0]
	m := dataset.MonthIndex(anio, mes)
	mix, _ := s.hist.Recent(id, m)
	ws, we := s.hist.Window(m)
	resp := PredictResponse{
		District: district, Ubigeo: s.cat.Ubigeos[id], Zone: dataset.ZoneNames[s.cat.Zones[id]], Mes: mes, Anio: anio,
		Population: s.pop[id], ModelVersion: st.Version, FeatureSet: string(st.FeatureSet), NodesUsed: used,
		TreesUsed: trees, ClusterMs: clusterMs, FranjaSupuesta: franjaSupuesta(&p, false), FranjaNote: franjaNote,
		RecentWindow: monthLabel(ws) + " a " + monthLabel(we),
	}
	if st.FeatureSet == dataset.FSPC4 {
		resp.Franja = &franja
	}
	for k := 0; k < dataset.NumClasses; k++ {
		resp.Probabilities = append(resp.Probabilities, ClassProb{Class: dataset.ClassNames[k], Probability: p[k]})
		resp.RecentMix = append(resp.RecentMix, ClassProb{Class: dataset.ClassNames[k], Probability: float64(mix[k]) / 100})
	}
	for _, k := range rank(p[:])[:3] {
		resp.Top3 = append(resp.Top3, ClassProb{Class: dataset.ClassNames[k], Probability: p[k]})
	}
	top, street := argmax(p[:], nil), topStreet(&p)
	resp.TopClass, resp.TopProbability = dataset.ClassNames[top], p[top]
	resp.TopStreetCrime, resp.TopStreetProbability = dataset.ClassNames[street], p[street]
	if b, err := json.Marshal(resp); err == nil {
		s.store.CacheSet(r.Context(), key, b, s.cfg.CacheTTL)
	}
	resp.LatencyMs = msSince(start)
	s.lat.add("predict_cluster", resp.LatencyMs)
	s.afterPredict(&resp, user)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) afterPredict(p *PredictResponse, user string) {
	s.persist(storage.CollPredictions, PredictionDoc{
		CreatedAt: time.Now().UTC(), User: user, District: p.District, Zone: p.Zone, Mes: p.Mes, Anio: p.Anio,
		Probabilities: p.Probabilities, TopClass: p.TopClass, TopProbability: p.TopProbability,
		TopStreetCrime: p.TopStreetCrime, ModelVersion: p.ModelVersion, FeatureSet: p.FeatureSet, NodesUsed: p.NodesUsed,
		TreesUsed: p.TreesUsed, FromCache: p.FromCache, LatencyMs: p.LatencyMs,
	})
	s.hub.publish(TopicPrediction, map[string]any{"user": user, "district": p.District, "mes": p.Mes, "anio": p.Anio,
		"topClass": p.TopClass, "topProbability": p.TopProbability, "topStreetCrime": p.TopStreetCrime,
		"latencyMs": p.LatencyMs, "fromCache": p.FromCache})
}

// ---------------------------------------------------------------- impacto social

const impactQuestion = "¿En qué distritos de Lima Metropolitana y Callao, en qué mes y para qué delitos de calle conviene " +
	"priorizar el patrullaje preventivo, y a cuántas personas cubre esa priorización?"

// impactReport calcula, para un mes, el riesgo de delitos de calle de los 50
// distritos con el bosque distribuido (una consulta por distrito, en un lote):
//
//	denuncias de calle esperadas = promedio mensual de los 12 meses previos × P(calle | distrito, mes)
//	tasa por 100 mil = esperadas / población INEI × 100,000
//
// Si el mes ya ocurrió (hasta septiembre de 2026), lo compara con lo real.
func (s *Server) impactReport(st *modelState, mes, anio, top int, orden string) (*ImpactResponse, error) {
	n := len(s.cat.Districts)
	qs := make([]protocol.Query, n)
	for i, d := range s.cat.Districts {
		qs[i] = protocol.Query{District: d, Mes: mes, Anio: anio}
	}
	t0 := time.Now()
	probs, used, trees, err := s.clusterPredict(st, qs, 10*time.Second)
	if err != nil {
		return nil, err
	}
	m := dataset.MonthIndex(anio, mes)
	ws, we := s.hist.Window(m)
	resp := &ImpactResponse{Question: impactQuestion, Anio: anio, Mes: mes, Orden: orden, ModelVersion: st.Version,
		FeatureSet: string(st.FeatureSet), HistoryFrom: monthLabel(ws), HistoryTo: monthLabel(we),
		NodesUsed: used, TreesUsed: trees, ClusterMs: msSince(t0), CreatedAt: time.Now().UTC()}
	rows := make([]ImpactRow, n)
	hasActual := true
	for i := range s.cat.Districts {
		p := probs[i]
		streetP := 0.0
		for k := 0; k < dataset.NumClasses; k++ {
			if dataset.StreetCrime[k] {
				streetP += p[k]
			}
		}
		pop := s.pop[i]
		if pp, ok := s.cfg.Population.Get(s.cat.Ubigeos[i], anio); ok {
			pop = pp
		}
		exp := s.hist.MonthlyAvg(i, m)
		row := ImpactRow{District: s.cat.Districts[i], Ubigeo: s.cat.Ubigeos[i], Zone: dataset.ZoneNames[s.cat.Zones[i]],
			Population: pop, ExpectedTotal: exp, StreetShare: streetP, ExpectedStreet: exp * streetP,
			FranjaSupuesta: franjaSupuesta(&p, true)}
		if pop > 0 {
			row.RatePer100k = row.ExpectedStreet / float64(pop) * 1e5
		}
		sc := topStreet(&p)
		row.TopStreetCrime, row.TopStreetProb = dataset.ClassNames[sc], p[sc]
		row.FranjaSugerida = topFranja(row.FranjaSupuesta)
		for k := 0; k < dataset.NumClasses; k++ {
			row.Probabilities = append(row.Probabilities, ClassProb{Class: dataset.ClassNames[k], Probability: p[k]})
		}
		if act, ok := s.hist.Actual(i, m); ok {
			var street, total int64
			for k, v := range act {
				total += v
				if dataset.StreetCrime[k] {
					street += v
				}
			}
			row.ActualStreet, row.ActualTotal = &street, &total
		} else {
			hasActual = false
		}
		rows[i] = row
	}
	key := func(r ImpactRow) float64 {
		if orden == "tasa" {
			return r.RatePer100k
		}
		return r.ExpectedStreet
	}
	sort.SliceStable(rows, func(a, b int) bool { return key(rows[a]) > key(rows[b]) })
	sum := ImpactSummary{TopN: top, Districts: n}
	for i := range rows {
		rows[i].Rank = i + 1
		sum.PopulationTotal += rows[i].Population
		sum.ExpectedStreetTotal += rows[i].ExpectedStreet
		if i < top {
			sum.PopulationTop += rows[i].Population
			sum.ExpectedStreetTop += rows[i].ExpectedStreet
		}
	}
	if sum.PopulationTotal > 0 {
		sum.PopulationPct = 100 * float64(sum.PopulationTop) / float64(sum.PopulationTotal)
	}
	if sum.ExpectedStreetTotal > 0 {
		sum.ExpectedCoveragePct = 100 * sum.ExpectedStreetTop / sum.ExpectedStreetTotal
	}
	resp.Summary = sum
	if hasActual {
		// Ranking real del mes (lo que habría elegido alguien que conociera el futuro).
		real := make([]int, n)
		for i := range real {
			real[i] = i
		}
		sort.SliceStable(real, func(a, b int) bool {
			ra, rb := rows[real[a]], rows[real[b]]
			if orden == "tasa" {
				return float64(*ra.ActualStreet)/float64(max(ra.Population, 1)) > float64(*rb.ActualStreet)/float64(max(rb.Population, 1))
			}
			return *ra.ActualStreet > *rb.ActualStreet
		})
		bt := &ImpactBacktest{}
		inTop := map[string]bool{}
		var oracle int64
		for pos, idx := range real {
			rows[idx].ActualRank = pos + 1
			bt.RealStreetTotal += *rows[idx].ActualStreet
			if pos < top {
				oracle += *rows[idx].ActualStreet
				bt.RealTopDistricts = append(bt.RealTopDistricts, rows[idx].District)
				inTop[rows[idx].District] = true
			}
		}
		for i := 0; i < top && i < n; i++ {
			bt.RealStreetTop += *rows[i].ActualStreet
			if inTop[rows[i].District] {
				bt.OverlapAtN++
			}
		}
		// Línea base sin modelo: los N distritos con más delitos de calle (o mayor tasa)
		// en los 12 meses previos. Permite medir cuánto aporta el bosque al ranking.
		hist := make([]int, n)
		histKey := make([]float64, n)
		for i := range rows {
			hist[i] = i
			id := s.cat.Index[rows[i].District]
			histKey[i] = s.hist.StreetAvg(id, m)
			if orden == "tasa" {
				histKey[i] /= float64(max(rows[i].Population, 1))
			}
		}
		sort.SliceStable(hist, func(a, b int) bool { return histKey[hist[a]] > histKey[hist[b]] })
		var histTop int64
		for i := 0; i < top && i < n; i++ {
			histTop += *rows[hist[i]].ActualStreet
		}
		if bt.RealStreetTotal > 0 {
			bt.RealCoveragePct = 100 * float64(bt.RealStreetTop) / float64(bt.RealStreetTotal)
			bt.OracleCoveragePct = 100 * float64(oracle) / float64(bt.RealStreetTotal)
			bt.HistoricalCoveragePct = 100 * float64(histTop) / float64(bt.RealStreetTotal)
		}
		resp.Backtest = bt
	}
	resp.Rows = rows
	resp.Notes = []string{
		"Delitos de calle: hurto, robo, extorsión y secuestro (la violencia familiar y la estafa no se disuaden con patrullaje en la vía pública).",
		"Volumen esperado: promedio mensual de denuncias del distrito en los 12 meses previos (" + resp.HistoryFrom + " a " + resp.HistoryTo + ").",
		"Población: proyección del INEI al 30 de junio del año consultado (para años posteriores a 2026 se usa 2026).",
		franjaNote,
	}
	return resp, nil
}

func (s *Server) handleImpact(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	mes, anio, msg := parseMonthYear(r)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", msg, "/api/v1/impacto?mes=10&anio=2026&top=10&orden=casos")
		return
	}
	top, orden, msg := s.topAndOrder(r)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", msg, "")
		return
	}
	st := s.current()
	if st == nil {
		writeErr(w, http.StatusConflict, "MODEL_NOT_TRAINED", "el modelo aún no ha sido entrenado", "ejecute POST /api/v1/train (rol admin)")
		return
	}
	if st.FeatureSet == dataset.FSPC4 {
		writeErr(w, http.StatusConflict, "MODEL_NOT_SUPPORTED", "el ranking de impacto requiere un modelo sin la franja imputada (tb2 o base)", "reentrene con featureSet tb2")
		return
	}
	s.nImpact.Add(1)
	key := fmt.Sprintf("tb2:impact:v%d:%d:%d:%d:%s", st.Version, anio, mes, top, orden)
	var resp *ImpactResponse
	if b, ok := s.store.CacheGet(r.Context(), key); ok {
		var c ImpactResponse
		if json.Unmarshal(b, &c) == nil {
			resp = &c
			resp.FromCache = true
		}
	}
	if resp == nil {
		var err error
		resp, err = s.impactReport(st, mes, anio, top, orden)
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, "CLUSTER_UNAVAILABLE", err.Error(), "")
			return
		}
		if b, err := json.Marshal(resp); err == nil {
			s.store.CacheSet(r.Context(), key, b, s.cfg.CacheTTL)
		}
		s.persist(storage.CollImpact, resp)
	}
	resp.LatencyMs = msSince(start)
	if resp.FromCache {
		s.lat.add("impact_cache", resp.LatencyMs)
	} else {
		s.lat.add("impact_cluster", resp.LatencyMs)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) topAndOrder(r *http.Request) (int, string, string) {
	q := r.URL.Query()
	top := 10
	if t := q.Get("top"); t != "" {
		n, err := strconv.Atoi(t)
		if err != nil || n < 1 || n > len(s.cat.Districts) {
			return 0, "", fmt.Sprintf("top debe estar entre 1 y %d", len(s.cat.Districts))
		}
		top = n
	}
	orden := q.Get("orden")
	switch orden {
	case "":
		orden = "casos"
	case "casos", "tasa":
	default:
		return 0, "", "orden debe ser casos o tasa"
	}
	return top, orden, ""
}

// handlePatrol (compatibilidad con la PC4): plan de patrullaje de los N
// distritos prioritarios del mes, con el delito de calle a vigilar y la franja
// sugerida según el supuesto horario (marcada como tal).
func (s *Server) handlePatrol(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	mes, anio, msg := parseMonthYear(r)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", msg, "/api/v1/patrullaje?mes=10&anio=2026&top=10")
		return
	}
	top, orden, msg := s.topAndOrder(r)
	if msg != "" {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", msg, "")
		return
	}
	st := s.current()
	if st == nil {
		writeErr(w, http.StatusConflict, "MODEL_NOT_TRAINED", "el modelo aún no ha sido entrenado", "ejecute POST /api/v1/train (rol admin)")
		return
	}
	if st.FeatureSet == dataset.FSPC4 {
		writeErr(w, http.StatusConflict, "MODEL_NOT_SUPPORTED", "el plan de patrullaje requiere un modelo tb2 o base", "")
		return
	}
	s.nPatrol.Add(1)
	key := fmt.Sprintf("tb2:patrol:v%d:%d:%d:%d:%s", st.Version, anio, mes, top, orden)
	var resp *ImpactResponse
	if b, ok := s.store.CacheGet(r.Context(), key); ok {
		var c ImpactResponse
		if json.Unmarshal(b, &c) == nil {
			resp = &c
			resp.FromCache = true
		}
	}
	if resp == nil {
		full, err := s.impactReport(st, mes, anio, top, orden)
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, "CLUSTER_UNAVAILABLE", err.Error(), "")
			return
		}
		full.Rows = full.Rows[:top]
		resp = full
		if b, err := json.Marshal(resp); err == nil {
			s.store.CacheSet(r.Context(), key, b, s.cfg.CacheTTL)
		}
		s.persist(storage.CollPatrol, resp)
	}
	resp.LatencyMs = msSince(start)
	if resp.FromCache {
		s.lat.add("patrol_cache", resp.LatencyMs)
	} else {
		s.lat.add("patrol_cluster", resp.LatencyMs)
	}
	writeJSON(w, http.StatusOK, resp)
}
