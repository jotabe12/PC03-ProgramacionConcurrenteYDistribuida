// Coordinador: API REST + WebSocket (HTTP :8080) y servidor TCP del clúster (:9090).
package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jotabe12/cc65-tb2-grupo3/internal/api"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/auth"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/cluster"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/dataset"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/logbuf"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/poblacion"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/storage"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/storage/dbstore"
	"github.com/jotabe12/cc65-tb2-grupo3/openapi"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func duration(key string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(key)); err == nil {
		return d
	}
	return def
}

func integer(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

func main() {
	httpAddr := flag.String("http", env("HTTP_ADDR", ":8080"), "dirección HTTP de la API")
	tcpAddr := flag.String("tcp", env("TCP_ADDR", ":9090"), "dirección TCP para los nodos")
	data := flag.String("data", env("DATA_PATH", "data/DATASET_Denuncias Policiales - Enero 2018 a Septiembre 2026.csv"), "CSV de denuncias")
	popPath := flag.String("poblacion", env("POBLACION_PATH", "data/6894980-peru-poblacion-total-proyectada-al-30-de-junio-de-cada-ano-segun-departamento-provincia-y-distrito-2018-2026.xlsx"), "XLSX del INEI")
	flag.Parse()

	// Los logs van a la consola y a un buffer circular que se consulta desde el panel admin.
	logs := logbuf.New(integer("LOG_BUFFER", 1000))
	log.SetOutput(io.MultiWriter(os.Stderr, logbuf.Writer{Ring: logs, Source: "coordinator"}))
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	// 1. Dataset: catálogo de distritos, historial y conjuntos de evaluación.
	t, err := dataset.Load(*data, 0)
	if err != nil {
		log.Fatalf("no se pudo leer el dataset %s: %v", *data, err)
	}
	cat, err := dataset.BuildCatalog(t)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("dataset: %d filas leídas, %d válidas, %d denuncias clasificables, %d distritos (%.1f ms, %d workers)",
		t.Stats.RowsRead, t.Stats.RowsValid, t.Stats.DenunciasValidas, len(cat.Districts), t.Stats.LoadMs, t.Stats.Workers)

	// 2. Población del INEI (tasa por 100 mil habitantes y población cubierta).
	pop, err := poblacion.Load(*popPath)
	if err != nil {
		log.Fatalf("no se pudo leer la población del INEI %s: %v", *popPath, err)
	}
	missing := 0
	for _, u := range cat.Ubigeos {
		if _, ok := pop.Get(u, dataset.TestYear); !ok {
			missing++
		}
	}
	log.Printf("población INEI: %d distritos (años %d-%d); %d de %d distritos del modelo sin población",
		len(pop.ByUbigeo), pop.Years[0], pop.Years[len(pop.Years)-1], missing, len(cat.Districts))

	// 3. Persistencia: MongoDB + Redis (o memoria si no están configurados).
	var store storage.Store
	if os.Getenv("MONGO_URI") == "" && os.Getenv("REDIS_ADDR") == "" {
		log.Printf("MONGO_URI y REDIS_ADDR vacíos: persistencia en memoria")
		store = storage.NewMemory()
	} else {
		store = dbstore.Open(dbstore.Config{MongoURI: os.Getenv("MONGO_URI"), MongoDB: env("MONGO_DB", "tb2_denuncias"),
			RedisAddr: os.Getenv("REDIS_ADDR"), RedisUser: os.Getenv("REDIS_USER"), RedisPassword: os.Getenv("REDIS_PASSWORD")}, log.Printf)
	}
	defer store.Close()

	// 4. Seguridad: JWT y usuarios iniciales.
	secret := []byte(os.Getenv("JWT_SECRET"))
	if len(secret) == 0 {
		secret = auth.RandomSecret()
		log.Printf("JWT_SECRET vacío: se generó una clave aleatoria (los tokens dejan de valer al reiniciar)")
	}
	signer, err := auth.NewSigner(secret, duration("JWT_TTL", 8*time.Hour))
	if err != nil {
		log.Fatal(err)
	}
	users := []api.SeedUser{
		{Username: env("ADMIN_USER", "admin"), Password: os.Getenv("ADMIN_PASSWORD"), Role: auth.RoleAdmin},
		{Username: env("ANALISTA_USER", "analista"), Password: os.Getenv("ANALISTA_PASSWORD"), Role: auth.RoleAnalista},
	}
	for _, u := range users {
		if u.Password == "" {
			log.Printf("advertencia: %s sin contraseña configurada (no podrá iniciar sesión)", u.Username)
		}
	}
	var origins []string
	for _, o := range strings.Split(env("CORS_ORIGINS", "http://localhost:8080,http://127.0.0.1:8080"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			origins = append(origins, o)
		}
	}

	// 5. Clúster TCP (topología estrella).
	coord := cluster.New()
	addr, err := coord.Listen(*tcpAddr)
	if err != nil {
		log.Fatalf("TCP: %v", err)
	}
	log.Printf("servidor TCP del clúster en %s", addr)
	defer coord.Close()

	srv, err := api.New(api.Config{
		CacheTTL: duration("CACHE_TTL", 10*time.Minute), TrainTimeout: duration("TRAIN_TIMEOUT", 5*time.Minute),
		PredictTimeout: duration("PREDICT_TIMEOUT", 3*time.Second), MetricsInterval: duration("METRICS_INTERVAL", 30*time.Second),
		LiveInterval: duration("LIVE_INTERVAL", 2*time.Second), ExpectedNodes: integer("EXPECTED_NODES", 3),
		AutoTrain: env("AUTO_TRAIN", "true") == "true", OpenAPISpec: openapi.Spec, Logf: log.Printf,
		Signer: signer, Users: users, CORSOrigins: origins, TrustProxy: env("TRUST_PROXY", "false") == "true",
		AnalystRPM: integer("ANALISTA_RPM", 600), LoginPerMin: integer("LOGIN_MAX_FALLOS", 10),
		Logs: logs, Population: pop,
	}, coord, store, t, cat)
	if err != nil {
		log.Fatal(err)
	}
	defer srv.Close()
	log.Printf("seguridad: JWT HS256 (vigencia %s), CORS %v, cuota analista %d/min, %d intentos de login por minuto",
		signer.TTL(), origins, integer("ANALISTA_RPM", 600), integer("LOGIN_MAX_FALLOS", 10))

	stop := make(chan struct{})
	coord.StartHeartbeat(duration("HEARTBEAT", 2*time.Second), stop)
	defer close(stop)

	httpSrv := &http.Server{Addr: *httpAddr, Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 30 * time.Second, WriteTimeout: 10 * time.Minute, IdleTimeout: 2 * time.Minute}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go func() {
		<-ctx.Done()
		sh, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = httpSrv.Shutdown(sh)
	}()
	log.Printf("API REST en %s (Swagger UI en /swagger, WebSocket en /ws)", *httpAddr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	log.Printf("coordinador detenido")
}
