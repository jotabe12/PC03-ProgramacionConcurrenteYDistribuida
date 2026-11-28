// loadtest: generador de carga concurrente para POST /api/v1/predict.
//
// Para cada nivel de concurrencia C lanza C goroutines (clientes) que toman
// peticiones de un canal compartido (Worker Pool) y miden la latencia de cada
// una. Cada nivel usa un año futuro distinto, así la primera pasada recorre
// 600 consultas nuevas (50 distritos × 12 meses: van al clúster) y la segunda
// las repite (salen de la caché de Redis).
//
//	loadtest -url http://coordinator:8080 -user admin -password ... -levels 10,50,100
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type result struct {
	ms       float64
	serverMs float64
	status   int
	cached   bool
}

type Summary struct {
	Scenario    string  `json:"scenario"`
	Clients     int     `json:"clients"`
	Requests    int     `json:"requests"`
	OK          int     `json:"ok"`
	Errors      int     `json:"errors"`
	FromCache   int     `json:"fromCache"`
	DurationS   float64 `json:"durationS"`
	Throughput  float64 `json:"throughputRps"`
	AvgMs       float64 `json:"avgMs"`
	P50Ms       float64 `json:"p50Ms"`
	P95Ms       float64 `json:"p95Ms"`
	P99Ms       float64 `json:"p99Ms"`
	MaxMs       float64 `json:"maxMs"`
	ServerP50Ms float64 `json:"serverP50Ms"`
	ServerP99Ms float64 `json:"serverP99Ms"`
	Under100Pct float64 `json:"pctUnder100ms"`
}

type Report struct {
	Target    string    `json:"target"`
	Endpoint  string    `json:"endpoint"`
	StartedAt time.Time `json:"startedAt"`
	GoVersion string    `json:"goVersion"`
	NumCPU    int       `json:"numCPU"`
	Note      string    `json:"note"`
	Results   []Summary `json:"results"`
}

func main() {
	base := flag.String("url", "http://coordinator:8080", "URL base de la API")
	user := flag.String("user", os.Getenv("ADMIN_USER"), "usuario")
	pass := flag.String("password", os.Getenv("ADMIN_PASSWORD"), "contraseña")
	levels := flag.String("levels", "10,50,100", "niveles de concurrencia")
	firstYear := flag.Int("year", 2027, "primer año (futuro) de las consultas; cada nivel usa el siguiente")
	flag.Parse()

	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxIdleConns: 512, MaxIdleConnsPerHost: 512, IdleConnTimeout: time.Minute}}
	token, err := login(client, *base, *user, *pass)
	if err != nil {
		fmt.Fprintln(os.Stderr, "login:", err)
		os.Exit(1)
	}
	districts, err := listDistricts(client, *base, token)
	if err != nil {
		fmt.Fprintln(os.Stderr, "districts:", err)
		os.Exit(1)
	}
	rep := Report{Target: *base, Endpoint: "POST /api/v1/predict", StartedAt: time.Now().UTC(), GoVersion: runtime.Version(),
		NumCPU: runtime.NumCPU(), Note: "Latencia medida en el cliente (incluye red Docker y JSON); serverMs = cabecera X-Response-Time-Ms."}
	year := *firstYear
	for _, l := range strings.Split(*levels, ",") {
		c, err := strconv.Atoi(strings.TrimSpace(l))
		if err != nil || c < 1 {
			continue
		}
		var bodies [][]byte
		for _, d := range districts {
			for m := 1; m <= 12; m++ {
				b, _ := json.Marshal(map[string]any{"district": d, "mes": m, "anio": year})
				bodies = append(bodies, b)
			}
		}
		for _, sc := range []string{"cluster (consultas nuevas)", "cache (mismas consultas)"} {
			s := run(client, *base, token, bodies, c)
			s.Scenario = sc
			rep.Results = append(rep.Results, s)
			fmt.Fprintf(os.Stderr, "%-28s %3d clientes: %5d ok, %3d err, %7.0f req/s, p50 %.2f ms, p95 %.2f ms, p99 %.2f ms, max %.2f ms\n",
				sc, c, s.OK, s.Errors, s.Throughput, s.P50Ms, s.P95Ms, s.P99Ms, s.MaxMs)
		}
		year++
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(rep)
}

func run(client *http.Client, base, token string, bodies [][]byte, clients int) Summary {
	jobs := make(chan []byte, len(bodies))
	for _, b := range bodies {
		jobs <- b
	}
	close(jobs)
	results := make(chan result, len(bodies))
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range jobs {
				results <- predict(client, base, token, b)
			}
		}()
	}
	wg.Wait()
	close(results)
	dur := time.Since(start).Seconds()
	var lat, srv []float64
	s := Summary{Clients: clients, Requests: len(bodies), DurationS: dur}
	under := 0
	for r := range results {
		if r.status != http.StatusOK {
			s.Errors++
			continue
		}
		s.OK++
		if r.cached {
			s.FromCache++
		}
		lat = append(lat, r.ms)
		srv = append(srv, r.serverMs)
		if r.ms < 100 {
			under++
		}
	}
	if len(lat) == 0 {
		return s
	}
	sort.Float64s(lat)
	sort.Float64s(srv)
	sum := 0.0
	for _, v := range lat {
		sum += v
	}
	pct := func(v []float64, p float64) float64 { return round(v[int(p*float64(len(v)-1)+0.5)]) }
	s.AvgMs, s.P50Ms, s.P95Ms, s.P99Ms, s.MaxMs = round(sum/float64(len(lat))), pct(lat, .5), pct(lat, .95), pct(lat, .99), round(lat[len(lat)-1])
	s.ServerP50Ms, s.ServerP99Ms = pct(srv, .5), pct(srv, .99)
	s.Throughput = round(float64(s.OK) / dur)
	s.Under100Pct = round(100 * float64(under) / float64(len(lat)))
	return s
}

func round(v float64) float64 { return float64(int64(v*1000+0.5)) / 1000 }

func predict(client *http.Client, base, token string, body []byte) result {
	req, _ := http.NewRequest(http.MethodPost, base+"/api/v1/predict", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	t0 := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return result{status: 0}
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	r := result{ms: float64(time.Since(t0).Microseconds()) / 1000, status: resp.StatusCode}
	r.serverMs, _ = strconv.ParseFloat(resp.Header.Get("X-Response-Time-Ms"), 64)
	var p struct {
		FromCache bool `json:"fromCache"`
	}
	if json.Unmarshal(b, &p) == nil {
		r.cached = p.FromCache
	}
	return r
}

func login(client *http.Client, base, user, pass string) (string, error) {
	b, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	resp, err := client.Post(base+"/api/v1/auth/login", "application/json", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var lr struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil || lr.Token == "" {
		return "", fmt.Errorf("respuesta %d sin token", resp.StatusCode)
	}
	return lr.Token, nil
}

func listDistricts(client *http.Client, base, token string) ([]string, error) {
	req, _ := http.NewRequest(http.MethodGet, base+"/api/v1/districts", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var d struct {
		Districts []struct {
			District string `json:"district"`
		} `json:"districts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return nil, err
	}
	out := make([]string, len(d.Districts))
	for i, x := range d.Districts {
		out[i] = x.District
	}
	return out, nil
}
