package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jotabe12/cc65-tb2-grupo3/internal/auth"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/cluster"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/dataset"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/forest"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/poblacion"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/storage"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/worker"
)

const (
	csvPath  = "../../data/DATASET_Denuncias Policiales - Enero 2018 a Septiembre 2026.csv"
	xlsxPath = "../../data/6894980-peru-poblacion-total-proyectada-al-30-de-junio-de-cada-ano-segun-departamento-provincia-y-distrito-2018-2026.xlsx"
	origin   = "http://localhost:8080"
	adminPW  = "Admin-Prueba-2026"
	anaPW    = "Analista-Prueba-2026"
)

type env struct {
	t      *testing.T
	http   *httptest.Server
	srv    *Server
	coord  *cluster.Coordinator
	store  *storage.Memory
	cancel map[string]context.CancelFunc
	addr   string
	admin  string // token
	ana    string // token
}

func setup(t *testing.T, mod func(*Config)) *env {
	for _, p := range []string{csvPath, xlsxPath} {
		if _, err := os.Stat(p); err != nil {
			t.Skip("datos no disponibles: " + p)
		}
	}
	tab, err := dataset.Load(csvPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	cat, _ := dataset.BuildCatalog(tab)
	pop, err := poblacion.Load(xlsxPath)
	if err != nil {
		t.Fatal(err)
	}
	coord := cluster.New()
	coord.Logf = func(string, ...any) {}
	a, err := coord.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := auth.NewSigner([]byte(strings.Repeat("s", 48)), time.Hour)
	cfg := Config{OpenAPISpec: []byte("openapi: 3.0.3"), Logf: func(string, ...any) {}, PredictTimeout: 5 * time.Second,
		Signer: signer, CORSOrigins: []string{origin}, AnalystRPM: 0, LoginPerMin: 5, Population: pop,
		LiveInterval: 200 * time.Millisecond,
		Users:        []SeedUser{{"admin", adminPW, auth.RoleAdmin}, {"analista", anaPW, auth.RoleAnalista}}}
	if mod != nil {
		mod(&cfg)
	}
	store := storage.NewMemory()
	srv, err := New(cfg, coord, store, tab, cat)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, srv: srv, coord: coord, store: store, cancel: map[string]context.CancelFunc{}, addr: a.String()}
	e.http = httptest.NewServer(srv.Handler())
	for i := 1; i <= 3; i++ {
		e.startNode(fmt.Sprintf("ml-node-%d", i))
	}
	e.waitNodes(3)
	t.Cleanup(func() {
		for _, c := range e.cancel {
			c()
		}
		e.http.Close()
		srv.Close()
		coord.Close()
	})
	e.admin = e.login("admin", adminPW)
	e.ana = e.login("analista", anaPW)
	return e
}

// startNode arranca un nodo nuevo (proceso "limpio", sin modelo).
func (e *env) startNode(id string) {
	ctx, cancel := context.WithCancel(context.Background())
	w := worker.New(id, e.addr, csvPath, 0)
	w.Logf = func(string, ...any) {}
	go w.RunForever(ctx)
	e.cancel[id] = cancel
}

func (e *env) waitNodes(n int) {
	deadline := time.Now().Add(10 * time.Second)
	for len(e.coord.NodeIDs()) != n {
		if time.Now().After(deadline) {
			e.t.Fatalf("se esperaban %d nodos, hay %d", n, len(e.coord.NodeIDs()))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (e *env) req(method, path, token string, body any, out any, hdr map[string]string) (int, http.Header) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.http.URL+path, r)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			e.t.Fatalf("%s %s: respuesta no JSON (%d): %s", method, path, resp.StatusCode, b)
		}
	}
	if resp.Header.Get("X-Response-Time-Ms") == "" {
		e.t.Errorf("%s %s sin cabecera X-Response-Time-Ms", method, path)
	}
	return resp.StatusCode, resp.Header
}

func (e *env) do(method, path string, body any, out any) int {
	code, _ := e.req(method, path, e.admin, body, out, nil)
	return code
}

func (e *env) login(user, pw string) string {
	var lr LoginResponse
	code, _ := e.req("POST", "/api/v1/auth/login", "", map[string]string{"username": user, "password": pw}, &lr, nil)
	if code != 200 || lr.Token == "" {
		e.t.Fatalf("login %s: %d", user, code)
	}
	return lr.Token
}

func q(d string, m, y int) map[string]any {
	return map[string]any{"district": d, "mes": m, "anio": y}
}

func TestEndToEnd(t *testing.T) {
	e := setup(t, nil)

	// 1. Antes de entrenar no se inventan probabilidades.
	var er ErrorResponse
	if code := e.do("POST", "/api/v1/predict", q("SAN JUAN DE LURIGANCHO", 10, 2026), &er); code != 409 || er.Code != "MODEL_NOT_TRAINED" {
		t.Fatalf("antes de entrenar: %d %+v", code, er)
	}

	// 2. Entrenamiento distribuido (modelo del TB2: variables reales + patrón reciente).
	var job TrainingJob
	start := time.Now()
	code := e.do("POST", "/api/v1/train", map[string]any{}, &job)
	if code != 200 || job.NodeCount != 3 || job.TotalTrees != 32 || job.TrainRecords != 1823335 || job.FeatureSet != "tb2" || len(job.Features) != 11 {
		t.Fatalf("train: %d %+v", code, job)
	}
	ev := job.Evaluation
	if ev == nil || ev.TestRecords != 164221 || ev.Accuracy <= ev.Baseline || ev.TreesUsed != 32 || ev.Top3Accuracy <= ev.Accuracy ||
		ev.Persistence <= ev.Baseline || ev.EvalYear != 2026 || len(ev.Importance) != 11 {
		t.Fatalf("evaluación: %+v (%s)", ev, job.EvalError)
	}
	t.Logf("train %v; exactitud %.2f%%, top-3 %.2f%%, F1 macro %.3f, persistencia %.2f%%, base %.2f%%", time.Since(start).Round(time.Millisecond),
		100*ev.Accuracy, 100*ev.Top3Accuracy, ev.MacroF1, 100*ev.Persistence, 100*ev.Baseline)

	// 3. Predicción del clúster y luego desde caché.
	var p1, p2 PredictResponse
	if code := e.do("POST", "/api/v1/predict", q("san juan de lurigancho", 10, 2026), &p1); code != 200 || p1.FromCache || p1.NodesUsed != 3 ||
		p1.TreesUsed != 32 || len(p1.Top3) != 3 || len(p1.FranjaSupuesta) != 4 || p1.Population < 1000000 || p1.Franja != nil {
		t.Fatalf("predict: %d %+v", code, p1)
	}
	sum := 0.0
	for _, c := range p1.Probabilities {
		sum += c.Probability
	}
	if sum < 0.999 || sum > 1.001 || p1.Probabilities[4].Probability <= 0 {
		t.Fatalf("probabilidades inválidas o extorsión en 0: %+v", p1.Probabilities)
	}
	if code := e.do("POST", "/api/v1/predict", q("SAN JUAN DE LURIGANCHO", 10, 2026), &p2); code != 200 || !p2.FromCache || p2.TopClass != p1.TopClass {
		t.Fatalf("caché: %d %+v", code, p2)
	}
	t.Logf("SJL 10/2026: %s %.3f (calle: %s %.3f); ventana %s; clúster %.3f ms, caché %.3f ms", p1.TopClass, p1.TopProbability,
		p1.TopStreetCrime, p1.TopStreetProbability, p1.RecentWindow, p1.LatencyMs, p2.LatencyMs)

	// 4. El mes y el año cambian la predicción.
	var p3 PredictResponse
	e.do("POST", "/api/v1/predict", q("SAN JUAN DE LURIGANCHO", 7, 2020), &p3)
	if p3.Probabilities[0].Probability == p1.Probabilities[0].Probability {
		t.Fatalf("mes/año no influyen en la predicción")
	}

	// 5. Validaciones.
	cases := []struct {
		body any
		code int
	}{
		{q("DISTRITO INVENTADO", 1, 2026), 404},
		{q("MIRAFLORES", 13, 2026), 400},
		{q("MIRAFLORES", 1, 1990), 400},
		{map[string]any{"district": "MIRAFLORES"}, 400},
		{map[string]any{"district": "MIRAFLORES", "mes": 1, "anio": 2026, "extra": 1}, 400},
	}
	for _, c := range cases {
		if code := e.do("POST", "/api/v1/predict", c.body, &er); code != c.code {
			t.Errorf("%v: se esperaba %d, se obtuvo %d", c.body, c.code, code)
		}
	}
	if code := e.do("GET", "/api/v1/predict", nil, nil); code != 405 {
		t.Errorf("GET /predict debe ser 405, fue %d", code)
	}

	// 6. Pregunta de impacto social: ranking de 50 distritos y verificación con lo real.
	var im, im2 ImpactResponse
	if code := e.do("GET", "/api/v1/impacto?mes=3&anio=2026&top=10", nil, &im); code != 200 || len(im.Rows) != 50 || im.FromCache ||
		im.Backtest == nil || im.Summary.PopulationTop <= 0 || im.Summary.ExpectedCoveragePct <= 20 {
		t.Fatalf("impacto: %d %+v", code, im.Summary)
	}
	e.do("GET", "/api/v1/impacto?mes=3&anio=2026&top=10", nil, &im2)
	if !im2.FromCache {
		t.Fatal("el impacto debe salir de caché la segunda vez")
	}
	bt := im.Backtest
	t.Logf("impacto mar-2026: top-10 cubre %.1f%% de los delitos de calle esperados y %.1f%% de la población (%d hab.); "+
		"real: %.1f%% de %d (óptimo %.1f%%), coincidencia %d/10; #1 %s", im.Summary.ExpectedCoveragePct, im.Summary.PopulationPct,
		im.Summary.PopulationTop, bt.RealCoveragePct, bt.RealStreetTotal, bt.OracleCoveragePct, bt.OverlapAtN, im.Rows[0].District)
	t.Logf("línea base sin modelo (ranking por los 12 meses previos): %.1f%%", bt.HistoricalCoveragePct)
	if bt.HistoricalCoveragePct <= 0 || bt.HistoricalCoveragePct > bt.OracleCoveragePct+1e-9 {
		t.Fatalf("línea base histórica fuera de rango: %.1f%% (óptimo %.1f%%)", bt.HistoricalCoveragePct, bt.OracleCoveragePct)
	}
	if bt.RealCoveragePct < 0.8*bt.OracleCoveragePct {
		t.Fatalf("el ranking cubre %.1f%% de lo real, muy lejos del óptimo %.1f%%", bt.RealCoveragePct, bt.OracleCoveragePct)
	}
	var fut ImpactResponse
	if code := e.do("GET", "/api/v1/impacto?mes=11&anio=2026&orden=tasa", nil, &fut); code != 200 || fut.Backtest != nil || fut.HistoryTo != "sep-2026" {
		t.Fatalf("impacto futuro: %d backtest=%v ventana %s-%s", code, fut.Backtest, fut.HistoryFrom, fut.HistoryTo)
	}
	var pr ImpactResponse
	if code := e.do("GET", "/api/v1/patrullaje?mes=10&anio=2026&top=5", nil, &pr); code != 200 || len(pr.Rows) != 5 || pr.Rows[0].FranjaSugerida == "" {
		t.Fatalf("patrullaje: %d %+v", code, pr)
	}

	// 7. Latencia: 40 predicciones distintas (sin caché) por debajo de 100 ms.
	worst := 0.0
	for i := 0; i < 40; i++ {
		var p PredictResponse
		d := []string{"LIMA", "COMAS", "ATE", "CALLAO", "MIRAFLORES"}[i%5]
		e.do("POST", "/api/v1/predict", q(d, 1+i%12, 2025-i/20), &p)
		if !p.FromCache && p.LatencyMs > worst {
			worst = p.LatencyMs
		}
	}
	if worst >= 100 {
		t.Fatalf("latencia del clúster %.1f ms >= 100 ms", worst)
	}
	t.Logf("peor latencia del clúster en 40 consultas: %.3f ms", worst)

	// 8. Tolerancia a fallos: cae un nodo, se sigue respondiendo con 2.
	e.cancel["ml-node-2"]()
	e.waitNodes(2)
	var p4 PredictResponse
	if code := e.do("POST", "/api/v1/predict", q("COMAS", 3, 2026), &p4); code != 200 || p4.NodesUsed != 2 || p4.TreesUsed != 21 {
		t.Fatalf("con un nodo caído: %d %+v", code, p4)
	}
	// 9. Vuelve como proceso nuevo (sin modelo): el coordinador lo reentrena solo.
	e.startNode("ml-node-2")
	e.waitNodes(3)
	deadline := time.Now().Add(30 * time.Second)
	for len(e.srv.modelInfo().NodesReady) != 3 {
		if time.Now().After(deadline) {
			t.Fatal("el nodo reincorporado no fue reentrenado")
		}
		time.Sleep(50 * time.Millisecond)
	}
	var p5 PredictResponse
	if code := e.do("POST", "/api/v1/predict", q("COMAS", 4, 2026), &p5); code != 200 || p5.NodesUsed != 3 || p5.TreesUsed != 32 {
		t.Fatalf("tras reincorporar: %d %+v", code, p5)
	}

	// 10. Métricas, logs y persistencia.
	var m MetricsResponse
	e.do("GET", "/api/v1/metrics", nil, &m)
	if len(m.Nodes) != 3 || m.Latency["predict_cluster"].Count == 0 || m.Requests["predictCacheHits"] == 0 {
		t.Fatalf("métricas incompletas: %+v", m)
	}
	time.Sleep(100 * time.Millisecond)
	st := e.store.Status(context.Background())
	if st.Collections[storage.CollTrainingJobs] != 1 || st.Collections[storage.CollPredictions] < 40 ||
		st.Collections[storage.CollImpact] != 2 || st.Collections[storage.CollPatrol] != 1 || st.Collections[storage.CollUsers] != 2 {
		t.Fatalf("persistencia: %+v", st.Collections)
	}
	var ev2 EvaluationResponse
	if code := e.do("GET", "/api/v1/evaluacion", nil, &ev2); code != 200 || ev2.Current == nil || len(ev2.History) != 1 {
		t.Fatalf("evaluación: %d %+v", code, ev2)
	}
	var h HealthResponse
	e.do("GET", "/api/v1/health", nil, &h)
	if h.Status != "ok" || !h.Model.Trained || len(h.Model.NodesReady) != 3 || h.Model.FeatureSet != "tb2" {
		t.Fatalf("health: %+v", h)
	}
}

// Políticas de seguridad: autenticación, roles, CORS, intentos de login y cuota.
func TestSecurityPolicies(t *testing.T) {
	e := setup(t, func(c *Config) { c.AnalystRPM = 20 })
	var er ErrorResponse
	// Sin token / token alterado -> 401.
	if code, h := e.req("GET", "/api/v1/districts", "", nil, &er, nil); code != 401 || er.Code != "UNAUTHORIZED" || h.Get("WWW-Authenticate") == "" {
		t.Fatalf("sin token: %d %+v", code, er)
	}
	if code, _ := e.req("GET", "/api/v1/districts", e.ana+"x", nil, &er, nil); code != 401 || er.Code != "INVALID_TOKEN" {
		t.Fatalf("token alterado: %d %+v", code, er)
	}
	// Rol analista: consulta sí, entrenar / métricas / logs no (403).
	var dist map[string]any
	if code, _ := e.req("GET", "/api/v1/districts", e.ana, nil, &dist, nil); code != 200 || dist["count"].(float64) != 50 {
		t.Fatalf("analista districts: %d", code)
	}
	for _, c := range []struct{ m, p string }{{"POST", "/api/v1/train"}, {"GET", "/api/v1/metrics"}, {"GET", "/api/v1/admin/logs"}} {
		if code, _ := e.req(c.m, c.p, e.ana, nil, &er, nil); code != 403 || er.Code != "FORBIDDEN" {
			t.Fatalf("analista %s %s: %d %+v", c.m, c.p, code, er)
		}
	}
	// /auth/me
	var me map[string]any
	if code, _ := e.req("GET", "/api/v1/auth/me", e.ana, nil, &me, nil); code != 200 || me["role"] != "analista" {
		t.Fatalf("me: %d %+v", code, me)
	}
	// CORS: origen permitido (preflight 204 con cabeceras) y origen ajeno (403).
	code, h := e.req("OPTIONS", "/api/v1/predict", "", nil, nil, map[string]string{"Origin": origin, "Access-Control-Request-Method": "POST"})
	if code != 204 || h.Get("Access-Control-Allow-Origin") != origin || !strings.Contains(h.Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("preflight permitido: %d %v", code, h)
	}
	if code, _ := e.req("GET", "/api/v1/districts", e.ana, nil, &er, map[string]string{"Origin": "http://evil.example"}); code != 403 || er.Code != "CORS_ORIGIN_DENIED" {
		t.Fatalf("origen ajeno: %d %+v", code, er)
	}
	// Login: contraseña incorrecta -> 401; tras 5 fallos desde la misma IP -> 429.
	for i := 0; i < 5; i++ {
		if code, _ := e.req("POST", "/api/v1/auth/login", "", map[string]string{"username": "admin", "password": "mala"}, &er, nil); code != 401 || er.Code != "INVALID_CREDENTIALS" {
			t.Fatalf("intento %d: %d %+v", i, code, er)
		}
	}
	if code, h := e.req("POST", "/api/v1/auth/login", "", map[string]string{"username": "admin", "password": adminPW}, &er, nil); code != 429 || h.Get("Retry-After") == "" {
		t.Fatalf("se esperaba 429 tras 5 fallos: %d %+v", code, er)
	}
	// Cuota del analista (20/min): la petición 21 seguida -> 429; el admin no tiene cuota.
	limited := 0
	for i := 0; i < 25; i++ {
		if code, _ := e.req("GET", "/api/v1/districts", e.ana, nil, nil, nil); code == 429 {
			limited++
		}
	}
	if limited < 3 {
		t.Fatalf("la cuota del analista no se aplicó (%d respuestas 429)", limited)
	}
	for i := 0; i < 30; i++ {
		if code := e.do("GET", "/api/v1/districts", nil, nil); code != 200 {
			t.Fatalf("el admin no debe tener cuota: %d", code)
		}
	}
	// Las contraseñas no se guardan en claro.
	u, err := e.store.GetUser(context.Background(), "admin")
	if err != nil || u.Hash == "" || strings.Contains(u.Hash, adminPW) || u.Iterations != auth.PBKDF2Iterations {
		t.Fatalf("usuario guardado: %+v %v", u, err)
	}
}

// ---------------------------------------------------------------- WebSocket

type wsTestConn struct {
	c  net.Conn
	br *bufio.Reader
}

func (e *env) dialWS(token string, originHdr string) (*wsTestConn, int) {
	c, err := net.Dial("tcp", strings.TrimPrefix(e.http.URL, "http://"))
	if err != nil {
		e.t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString([]byte("abcdefghijklmnop"))
	req := "GET /ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: " + key +
		"\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: jwt, " + token + "\r\n"
	if originHdr != "" {
		req += "Origin: " + originHdr + "\r\n"
	}
	c.Write([]byte(req + "\r\n"))
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		e.t.Fatal(err)
	}
	if resp.StatusCode != 101 {
		c.Close()
		return nil, resp.StatusCode
	}
	if resp.Header.Get("Sec-WebSocket-Protocol") != "jwt" {
		e.t.Fatalf("subprotocolo: %q", resp.Header.Get("Sec-WebSocket-Protocol"))
	}
	return &wsTestConn{c: c, br: br}, 101
}

func (w *wsTestConn) next(t *testing.T, timeout time.Duration) Event {
	t.Helper()
	_ = w.c.SetReadDeadline(time.Now().Add(timeout))
	for {
		var h [2]byte
		if _, err := io.ReadFull(w.br, h[:]); err != nil {
			t.Fatalf("lectura WS: %v", err)
		}
		n := int(h[1] & 0x7F)
		switch n {
		case 126:
			var b [2]byte
			io.ReadFull(w.br, b[:])
			n = int(binary.BigEndian.Uint16(b[:]))
		case 127:
			var b [8]byte
			io.ReadFull(w.br, b[:])
			n = int(binary.BigEndian.Uint64(b[:]))
		}
		p := make([]byte, n)
		io.ReadFull(w.br, p)
		if h[0]&0x0F != 0x1 {
			continue // ping u otras tramas de control
		}
		var ev Event
		if err := json.Unmarshal(p, &ev); err != nil {
			t.Fatalf("evento no JSON: %s", p)
		}
		return ev
	}
}

func (w *wsTestConn) send(payload []byte) {
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	f := []byte{0x81, 0x80 | byte(len(payload))}
	f = append(f, mask[:]...)
	for i, b := range payload {
		f = append(f, b^mask[i%4])
	}
	w.c.Write(f)
}

func TestWebSocketRealTime(t *testing.T) {
	e := setup(t, nil)
	// Sin token válido u origen ajeno: no se abre.
	if _, code := e.dialWS("token-falso", ""); code != 401 {
		t.Fatalf("token falso: %d", code)
	}
	if _, code := e.dialWS(e.admin, "http://evil.example"); code != 403 {
		t.Fatalf("origen ajeno: %d", code)
	}
	adm, _ := e.dialWS(e.admin, origin)
	defer adm.c.Close()
	if ev := adm.next(t, 3*time.Second); ev.Type != "hello" {
		t.Fatalf("primer evento: %+v", ev)
	}
	ana, _ := e.dialWS(e.ana, "")
	defer ana.c.Close()
	if ev := ana.next(t, 3*time.Second); ev.Type != "hello" {
		t.Fatalf("primer evento analista: %+v", ev)
	}
	ana.send([]byte(`{"subscribe":["logs","metrics"]}`)) // no permitido para analista: se ignora

	go e.do("POST", "/api/v1/train", map[string]any{"trees": 9, "workers": 2}, nil)
	seen := map[string]bool{}
	deadline := time.Now().Add(20 * time.Second)
	for !(seen["training:inicio"] && seen["training:progreso"] && seen["training:fin"] && seen["metrics"] && seen["logs"]) {
		if time.Now().After(deadline) {
			t.Fatalf("eventos recibidos por el admin: %v", seen)
		}
		ev := adm.next(t, 10*time.Second)
		key := ev.Type
		if m, ok := ev.Data.(map[string]any); ok && ev.Type == "training" {
			key += ":" + fmt.Sprint(m["event"])
		}
		seen[key] = true
	}
	// El analista recibe el entrenamiento pero nunca métricas ni logs.
	for i := 0; i < 5; i++ {
		ev := ana.next(t, 10*time.Second)
		if ev.Type == "metrics" || ev.Type == "logs" {
			t.Fatalf("el analista recibió %s", ev.Type)
		}
		if ev.Type == "training" {
			if m := ev.Data.(map[string]any); m["event"] == "fin" {
				break
			}
		}
	}
	// Predicción en vivo.
	e.req("POST", "/api/v1/predict", e.ana, q("ATE", 5, 2026), nil, nil)
	for {
		ev := adm.next(t, 5*time.Second)
		if ev.Type == "prediction" {
			if m := ev.Data.(map[string]any); m["district"] != "ATE" || m["user"] != "analista" {
				t.Fatalf("predicción en vivo: %+v", m)
			}
			break
		}
	}
}

// Opciones de entrenamiento: nodos, modo de validación y conjunto pc4 (ablation).
func TestTrainOptions(t *testing.T) {
	e := setup(t, nil)
	e.srv.lastVersion = 41 // como si MongoDB ya tuviera hasta v41 de una ejecución anterior
	var job TrainingJob
	if code := e.do("POST", "/api/v1/train", map[string]any{"nodes": 1, "trees": 4, "workers": 2}, &job); code != 200 ||
		job.NodeCount != 1 || job.Nodes[0].Result.Shards != 1 || job.TrainRecords != 1823335 {
		t.Fatalf("1 nodo: %d %+v", code, job)
	}
	if job.Version != 42 {
		t.Fatalf("la numeración debe continuar tras un reinicio: v%d, se esperaba v42", job.Version)
	}
	var er ErrorResponse
	if code := e.do("POST", "/api/v1/train", map[string]any{"nodes": 5}, &er); code != 409 || er.Code != "NOT_ENOUGH_NODES" {
		t.Fatalf("5 nodos: %d %+v", code, er)
	}
	if code := e.do("POST", "/api/v1/train", map[string]any{"featureSet": "xyz"}, &er); code != 400 {
		t.Fatalf("featureSet inválido: %d", code)
	}
	if code := e.do("POST", "/api/v1/train", map[string]any{"mode": "validacion", "trees": 6}, &job); code != 200 ||
		job.Evaluation.EvalYear != 2025 || job.TrainTo != 2024 {
		t.Fatalf("validación: %d %+v", code, job.Evaluation)
	}
	if code := e.do("POST", "/api/v1/train", map[string]any{"featureSet": "pc4", "trees": 6, "classWeight": "sqrt"}, &job); code != 200 ||
		len(job.Features) != 5 || job.Evaluation.Queries != 1760 || job.Config.ClassWeight != "sqrt" {
		t.Fatalf("pc4: %d %+v", code, job)
	}
	if code := e.do("POST", "/api/v1/predict", q("LIMA", 1, 2026), &er); code != 400 {
		t.Fatalf("pc4 sin franja: %d", code)
	}
	var p PredictResponse
	body := map[string]any{"district": "LIMA", "mes": 1, "anio": 2026, "franja": 3}
	if code := e.do("POST", "/api/v1/predict", body, &p); code != 200 || p.Franja == nil || *p.Franja != 3 {
		t.Fatalf("pc4 con franja: %d %+v", code, p)
	}
	if code := e.do("GET", "/api/v1/impacto?mes=1&anio=2026", nil, &er); code != 409 {
		t.Fatalf("impacto con pc4: %d", code)
	}
	var ev EvaluationResponse
	e.do("GET", "/api/v1/evaluacion", nil, &ev)
	if len(ev.History) != 3 || ev.History[0].FeatureSet != "pc4" {
		t.Fatalf("historial: %+v", ev.History)
	}
}

// El bosque distribuido debe dar EXACTAMENTE las mismas probabilidades que
// los mismos 32 árboles entrenados en un solo proceso (equivalencia).
func TestDistributedEqualsLocal(t *testing.T) {
	e := setup(t, nil)
	var job TrainingJob
	e.do("POST", "/api/v1/train", nil, &job)
	tab, _ := dataset.Load(csvPath, 0)
	cat, _ := dataset.BuildCatalog(tab)
	h := dataset.BuildHistory(tab, cat)
	sch := dataset.SchemaFor(dataset.FSTB2, cat)
	var local []*forest.Model
	for _, n := range job.Nodes {
		m := dataset.Expand(tab, cat, h, dataset.FSTB2, n.Result.Shard, n.Result.Shards, func(y int) bool { return y < dataset.TestYear })
		local = append(local, forest.Train(m, sch, n.Result.TreeIDs, forest.DefaultConfig(), 1, nil))
	}
	for _, d := range []string{"SAN JUAN DE LURIGANCHO", "LIMA", "VENTANILLA", "BREÑA"} {
		var p PredictResponse
		e.do("POST", "/api/v1/predict", q(d, 9, 2026), &p)
		id, _ := cat.Lookup(d)
		x := dataset.Vector(dataset.FSTB2, cat, h, id, 0, 9, 2026)
		var want [6]float64
		for _, m := range local {
			pp := m.Predict(x)
			for k := range want {
				want[k] += pp[k] * float64(len(m.Trees)) / 32
			}
		}
		for k := range want {
			if diff := want[k] - p.Probabilities[k].Probability; diff > 1e-9 || diff < -1e-9 {
				t.Fatalf("%s clase %d: distribuido %.12f vs local %.12f", d, k, p.Probabilities[k].Probability, want[k])
			}
		}
	}
}
