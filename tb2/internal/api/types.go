package api

import (
	"time"

	"github.com/jotabe12/cc65-tb2-grupo3/internal/cluster"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/dataset"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/forest"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/protocol"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/storage"
)

// ErrorResponse es el cuerpo de todos los errores de la API.
type ErrorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code"`
	Hint  string `json:"hint,omitempty"`
}

// ---------------------------------------------------------------- autenticación

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Token     string    `json:"token"`
	TokenType string    `json:"tokenType"`
	ExpiresAt time.Time `json:"expiresAt"`
	User      UserInfo  `json:"user"`
}

type UserInfo struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

// ---------------------------------------------------------------- entrenamiento

// TrainRequest (todos los campos son opcionales; por defecto = PC3 + variables del TB2).
type TrainRequest struct {
	Trees       *int     `json:"trees,omitempty"`
	MaxDepth    *int     `json:"maxDepth,omitempty"`
	MinLeaf     *int     `json:"minLeaf,omitempty"`
	Mtry        *int     `json:"mtry,omitempty"`
	SampleFrac  *float64 `json:"sampleFrac,omitempty"`
	Workers     *int     `json:"workers,omitempty"`
	Nodes       *int     `json:"nodes,omitempty"`       // cuántos nodos usar (por defecto, todos)
	FeatureSet  *string  `json:"featureSet,omitempty"`  // tb2 | base | pc4
	ClassWeight *string  `json:"classWeight,omitempty"` // none | sqrt | balanced
	Mode        *string  `json:"mode,omitempty"`        // final (2018-2025 -> 2026) | validacion (2018-2024 -> 2025)
}

type NodeTrain struct {
	NodeID      string               `json:"nodeId" bson:"nodeId"`
	RoundTripMs float64              `json:"roundTripMs" bson:"roundTripMs"`
	Result      protocol.TrainResult `json:"result" bson:"result"`
}

type ClassMetric struct {
	Class     string  `json:"class" bson:"class"`
	Real      int64   `json:"real" bson:"real"`
	Predicted int64   `json:"predicted" bson:"predicted"`
	Precision float64 `json:"precision" bson:"precision"`
	Recall    float64 `json:"recall" bson:"recall"`
	F1        float64 `json:"f1" bson:"f1"`
}

type FeatureImportance struct {
	Feature    string  `json:"feature" bson:"feature"`
	Importance float64 `json:"importance" bson:"importance"`
}

// Evaluation mide el bosque distribuido en el año de evaluación (denuncias
// ponderadas: cada vector de entrada cuenta tantas veces como denuncias tiene).
type Evaluation struct {
	EvalYear      int                 `json:"evalYear" bson:"evalYear"`
	FeatureSet    string              `json:"featureSet" bson:"featureSet"`
	TestRecords   int64               `json:"testRecords" bson:"testRecords"`
	Queries       int                 `json:"distinctQueries" bson:"distinctQueries"`
	Accuracy      float64             `json:"accuracy" bson:"accuracy"`
	Top3Accuracy  float64             `json:"top3Accuracy" bson:"top3Accuracy"`
	Baseline      float64             `json:"baselineAccuracy" bson:"baselineAccuracy"`
	BaselineClass string              `json:"baselineClass" bson:"baselineClass"`
	Persistence   float64             `json:"persistenceAccuracy" bson:"persistenceAccuracy"`
	MacroRecall   float64             `json:"macroRecall" bson:"macroRecall"`
	MacroF1       float64             `json:"macroF1" bson:"macroF1"`
	WeightedF1    float64             `json:"weightedF1" bson:"weightedF1"`
	PerClass      []ClassMetric       `json:"perClass" bson:"perClass"`
	Classes       []string            `json:"classes" bson:"classes"`
	Confusion     [6][6]int64         `json:"confusion" bson:"confusion"` // [real][predicha]
	Importance    []FeatureImportance `json:"featureImportance" bson:"featureImportance"`
	NodesUsed     int                 `json:"nodesUsed" bson:"nodesUsed"`
	TreesUsed     int                 `json:"treesUsed" bson:"treesUsed"`
	EvalMs        float64             `json:"evalMs" bson:"evalMs"`
}

// TrainingJob se devuelve en POST /train y se guarda en training_jobs.
type TrainingJob struct {
	JobID        string        `json:"jobId" bson:"jobId"`
	Version      int64         `json:"version" bson:"version"`
	Status       string        `json:"status" bson:"status"`
	RequestedBy  string        `json:"requestedBy" bson:"requestedBy"`
	CreatedAt    time.Time     `json:"createdAt" bson:"createdAt"`
	Mode         string        `json:"mode" bson:"mode"`
	FeatureSet   string        `json:"featureSet" bson:"featureSet"`
	Features     []string      `json:"features" bson:"features"`
	TrainFrom    int           `json:"trainFrom" bson:"trainFrom"`
	TrainTo      int           `json:"trainTo" bson:"trainTo"`
	DurationMs   float64       `json:"durationMs" bson:"durationMs"` // reloj de pared en el coordinador (fan-out + fan-in)
	Nodes        []NodeTrain   `json:"nodes" bson:"nodes"`
	NodeCount    int           `json:"nodeCount" bson:"nodeCount"`
	Workers      int           `json:"workers" bson:"workers"`
	TotalTrees   int           `json:"totalTrees" bson:"totalTrees"`
	TrainRecords int64         `json:"trainRecords" bson:"trainRecords"`
	MaxTrainMs   float64       `json:"maxTrainMs" bson:"maxTrainMs"`   // nodo más lento: solo construcción de árboles
	MaxExpandMs  float64       `json:"maxExpandMs" bson:"maxExpandMs"` // nodo más lento: desagregación del shard
	Config       forest.Config `json:"config" bson:"config"`
	Dataset      dataset.Stats `json:"dataset" bson:"dataset"`
	Evaluation   *Evaluation   `json:"evaluation,omitempty" bson:"evaluation,omitempty"`
	EvalError    string        `json:"evaluationError,omitempty" bson:"evaluationError,omitempty"`
}

// JobSummary es una fila del historial de entrenamientos (GET /evaluacion).
type JobSummary struct {
	JobID       string        `json:"jobId"`
	Version     int64         `json:"version"`
	CreatedAt   time.Time     `json:"createdAt"`
	Mode        string        `json:"mode"`
	FeatureSet  string        `json:"featureSet"`
	ClassWeight string        `json:"classWeight"`
	NodeCount   int           `json:"nodeCount"`
	Workers     int           `json:"workers"`
	TotalTrees  int           `json:"totalTrees"`
	Config      forest.Config `json:"config"`
	DurationMs  float64       `json:"durationMs"`
	MaxTrainMs  float64       `json:"maxTrainMs"`
	EvalYear    int           `json:"evalYear"`
	Accuracy    float64       `json:"accuracy"`
	Top3        float64       `json:"top3Accuracy"`
	MacroF1     float64       `json:"macroF1"`
	Baseline    float64       `json:"baselineAccuracy"`
	Persistence float64       `json:"persistenceAccuracy"`
}

type EvaluationResponse struct {
	Model   ModelInfo    `json:"model"`
	Current *Evaluation  `json:"current"`
	History []JobSummary `json:"history"`
}

// ---------------------------------------------------------------- predicción

type PredictRequest struct {
	District *string `json:"district"`
	Mes      *int    `json:"mes"`
	Anio     *int    `json:"anio"`
	Franja   *int    `json:"franja,omitempty"` // solo si el modelo vigente usa el conjunto pc4
}

type ClassProb struct {
	Class       string  `json:"class" bson:"class"`
	Probability float64 `json:"probability" bson:"probability"`
}

type FranjaProb struct {
	Franja      string  `json:"franja" bson:"franja"`
	Probability float64 `json:"probability" bson:"probability"`
}

type PredictResponse struct {
	District             string       `json:"district"`
	Ubigeo               string       `json:"ubigeo"`
	Zone                 string       `json:"zone"`
	Mes                  int          `json:"mes"`
	Anio                 int          `json:"anio"`
	Franja               *int         `json:"franja,omitempty"`
	Population           int64        `json:"population"`
	Probabilities        []ClassProb  `json:"probabilities"`
	Top3                 []ClassProb  `json:"top3"`
	TopClass             string       `json:"topClass"`
	TopProbability       float64      `json:"topProbability"`
	TopStreetCrime       string       `json:"topStreetCrime"`
	TopStreetProbability float64      `json:"topStreetProbability"`
	RecentMix            []ClassProb  `json:"recentMix"` // patrón de los 12 meses previos (dato real)
	RecentWindow         string       `json:"recentWindow"`
	FranjaSupuesta       []FranjaProb `json:"franjaSupuesta"`
	FranjaNote           string       `json:"franjaNote"`
	ModelVersion         int64        `json:"modelVersion"`
	FeatureSet           string       `json:"featureSet"`
	NodesUsed            int          `json:"nodesUsed"`
	TreesUsed            int          `json:"treesUsed"`
	FromCache            bool         `json:"fromCache"`
	ClusterMs            float64      `json:"clusterMs"`
	LatencyMs            float64      `json:"latencyMs"`
}

// PredictionDoc es el documento de la colección predictions.
type PredictionDoc struct {
	CreatedAt      time.Time   `bson:"createdAt"`
	User           string      `bson:"user"`
	District       string      `bson:"district"`
	Zone           string      `bson:"zone"`
	Mes            int         `bson:"mes"`
	Anio           int         `bson:"anio"`
	Probabilities  []ClassProb `bson:"probabilities"`
	TopClass       string      `bson:"topClass"`
	TopProbability float64     `bson:"topProbability"`
	TopStreetCrime string      `bson:"topStreetCrime"`
	ModelVersion   int64       `bson:"modelVersion"`
	FeatureSet     string      `bson:"featureSet"`
	NodesUsed      int         `bson:"nodesUsed"`
	TreesUsed      int         `bson:"treesUsed"`
	FromCache      bool        `bson:"fromCache"`
	LatencyMs      float64     `bson:"latencyMs"`
}

// ---------------------------------------------------------------- impacto y patrullaje

// ImpactRow es un distrito en el ranking de riesgo del mes consultado.
type ImpactRow struct {
	Rank           int          `json:"rank" bson:"rank"`
	District       string       `json:"district" bson:"district"`
	Ubigeo         string       `json:"ubigeo" bson:"ubigeo"`
	Zone           string       `json:"zone" bson:"zone"`
	Population     int64        `json:"population" bson:"population"`
	ExpectedTotal  float64      `json:"expectedTotal" bson:"expectedTotal"`   // promedio mensual de los 12 meses previos
	StreetShare    float64      `json:"streetShare" bson:"streetShare"`       // P(delito de calle) del bosque
	ExpectedStreet float64      `json:"expectedStreet" bson:"expectedStreet"` // denuncias de calle esperadas en el mes
	RatePer100k    float64      `json:"ratePer100k" bson:"ratePer100k"`
	TopStreetCrime string       `json:"topStreetCrime" bson:"topStreetCrime"`
	TopStreetProb  float64      `json:"topStreetProbability" bson:"topStreetProbability"`
	Probabilities  []ClassProb  `json:"probabilities" bson:"probabilities"`
	FranjaSugerida string       `json:"franjaSugerida" bson:"franjaSugerida"`
	FranjaSupuesta []FranjaProb `json:"franjaSupuesta" bson:"franjaSupuesta"`
	ActualStreet   *int64       `json:"actualStreet,omitempty" bson:"actualStreet,omitempty"` // dato real (si el mes ya ocurrió)
	ActualTotal    *int64       `json:"actualTotal,omitempty" bson:"actualTotal,omitempty"`
	ActualRank     int          `json:"actualRank,omitempty" bson:"actualRank,omitempty"`
}

type ImpactSummary struct {
	TopN                int     `json:"topN" bson:"topN"`
	Districts           int     `json:"districts" bson:"districts"`
	PopulationTop       int64   `json:"populationTop" bson:"populationTop"`
	PopulationTotal     int64   `json:"populationTotal" bson:"populationTotal"`
	PopulationPct       float64 `json:"populationPct" bson:"populationPct"`
	ExpectedStreetTop   float64 `json:"expectedStreetTop" bson:"expectedStreetTop"`
	ExpectedStreetTotal float64 `json:"expectedStreetTotal" bson:"expectedStreetTotal"`
	ExpectedCoveragePct float64 `json:"expectedCoveragePct" bson:"expectedCoveragePct"`
}

// ImpactBacktest compara el ranking con lo que realmente ocurrió (si hay datos del mes).
type ImpactBacktest struct {
	RealStreetTop     int64    `json:"realStreetTop" bson:"realStreetTop"`
	RealStreetTotal   int64    `json:"realStreetTotal" bson:"realStreetTotal"`
	RealCoveragePct   float64  `json:"realCoveragePct" bson:"realCoveragePct"`
	OracleCoveragePct float64  `json:"oracleCoveragePct" bson:"oracleCoveragePct"` // si se hubiera conocido el futuro
	OverlapAtN        int      `json:"overlapAtN" bson:"overlapAtN"`
	RealTopDistricts  []string `json:"realTopDistricts" bson:"realTopDistricts"`
}

type ImpactResponse struct {
	Question     string          `json:"question" bson:"question"`
	Anio         int             `json:"anio" bson:"anio"`
	Mes          int             `json:"mes" bson:"mes"`
	Orden        string          `json:"orden" bson:"orden"`
	ModelVersion int64           `json:"modelVersion" bson:"modelVersion"`
	FeatureSet   string          `json:"featureSet" bson:"featureSet"`
	HistoryFrom  string          `json:"historyFrom" bson:"historyFrom"`
	HistoryTo    string          `json:"historyTo" bson:"historyTo"`
	Summary      ImpactSummary   `json:"summary" bson:"summary"`
	Backtest     *ImpactBacktest `json:"backtest,omitempty" bson:"backtest,omitempty"`
	Rows         []ImpactRow     `json:"rows" bson:"rows"`
	Notes        []string        `json:"notes" bson:"notes"`
	NodesUsed    int             `json:"nodesUsed" bson:"nodesUsed"`
	TreesUsed    int             `json:"treesUsed" bson:"treesUsed"`
	FromCache    bool            `json:"fromCache" bson:"fromCache"`
	ClusterMs    float64         `json:"clusterMs" bson:"clusterMs"`
	LatencyMs    float64         `json:"latencyMs" bson:"latencyMs"`
	CreatedAt    time.Time       `json:"createdAt" bson:"createdAt"`
}

type DistrictInfo struct {
	District   string `json:"district"`
	Ubigeo     string `json:"ubigeo"`
	Zone       string `json:"zone"`
	Population int64  `json:"population"`
	Denuncias  int64  `json:"denuncias"`
	Street     int64  `json:"denunciasCalle"`
}

// ---------------------------------------------------------------- estado y métricas

type ModelInfo struct {
	Trained    bool           `json:"trained" bson:"trained"`
	Version    int64          `json:"version,omitempty" bson:"version,omitempty"`
	TrainedAt  *time.Time     `json:"trainedAt,omitempty" bson:"trainedAt,omitempty"`
	TotalTrees int            `json:"totalTrees,omitempty" bson:"totalTrees,omitempty"`
	NodesReady []string       `json:"nodesReady" bson:"nodesReady"`
	Accuracy   float64        `json:"accuracy,omitempty" bson:"accuracy,omitempty"`
	Top3       float64        `json:"top3Accuracy,omitempty" bson:"top3Accuracy,omitempty"`
	FeatureSet string         `json:"featureSet,omitempty" bson:"featureSet,omitempty"`
	Mode       string         `json:"mode,omitempty" bson:"mode,omitempty"`
	EvalYear   int            `json:"evalYear,omitempty" bson:"evalYear,omitempty"`
	Config     *forest.Config `json:"config,omitempty" bson:"config,omitempty"`
}

type HealthResponse struct {
	Status         string         `json:"status"`
	Time           time.Time      `json:"time"`
	UptimeS        float64        `json:"uptimeS"`
	NodesExpected  int            `json:"nodesExpected"`
	NodesConnected int            `json:"nodesConnected"`
	Nodes          []string       `json:"nodes"`
	Model          ModelInfo      `json:"model"`
	Storage        storage.Status `json:"storage"`
}

type LatencySummary struct {
	Count    int     `json:"count" bson:"count"`
	AvgMs    float64 `json:"avgMs" bson:"avgMs"`
	P50Ms    float64 `json:"p50Ms" bson:"p50Ms"`
	P95Ms    float64 `json:"p95Ms" bson:"p95Ms"`
	P99Ms    float64 `json:"p99Ms" bson:"p99Ms"`
	MaxMs    float64 `json:"maxMs" bson:"maxMs"`
	Under100 float64 `json:"pctUnder100ms" bson:"pctUnder100ms"`
}

type MetricsResponse struct {
	Timestamp   time.Time                 `json:"timestamp" bson:"timestamp"`
	UptimeS     float64                   `json:"uptimeS" bson:"uptimeS"`
	Coordinator map[string]any            `json:"coordinator" bson:"coordinator"`
	Nodes       []cluster.NodeInfo        `json:"nodes" bson:"nodes"`
	Model       ModelInfo                 `json:"model" bson:"model"`
	Requests    map[string]int64          `json:"requests" bson:"requests"`
	CacheHitPct float64                   `json:"cacheHitPct" bson:"cacheHitPct"`
	Latency     map[string]LatencySummary `json:"latency" bson:"latency"`
	WebSocket   map[string]int64          `json:"websocket" bson:"websocket"`
	Storage     storage.Status            `json:"storage" bson:"storage"`
}
