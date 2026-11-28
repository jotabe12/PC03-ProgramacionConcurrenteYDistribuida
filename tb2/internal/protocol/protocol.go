// Package protocol define los mensajes JSON que viajan por TCP entre el
// coordinador y los nodos ML (un objeto JSON por mensaje, json.Encoder/Decoder).
package protocol

import (
	"time"

	"github.com/jotabe12/cc65-tb2-grupo3/internal/forest"
)

// Tipos de mensaje.
const (
	TypeRegister = "REGISTER" // nodo -> coordinador (al conectarse)
	TypeTrain    = "TRAIN"    // coordinador -> nodo
	TypePredict  = "PREDICT"  // coordinador -> nodo (lote de consultas)
	TypePing     = "PING"     // coordinador -> nodo (latido)

	TypeTrainOK   = "TRAIN_OK"
	TypePredictOK = "PREDICT_OK"
	TypePong      = "PONG"
	TypeError     = "ERROR"

	// Mensajes espontáneos del nodo (sin ID de petición): el coordinador los
	// reenvía por WebSocket al frontend.
	TypeProgress = "PROGRESS" // avance del entrenamiento
	TypeLog      = "LOG"      // línea de log del nodo
)

// Códigos de error de los nodos.
const (
	CodeNotTrained      = "NOT_TRAINED"
	CodeUnknownDistrict = "UNKNOWN_DISTRICT"
	CodeBadRequest      = "BAD_REQUEST"
	CodeInternal        = "INTERNAL"
)

// Message es el sobre común. ID correlaciona cada respuesta con su petición:
// una respuesta tardía con un ID que ya nadie espera se descarta.
type Message struct {
	Type   string `json:"type"`
	ID     string `json:"id,omitempty"`
	NodeID string `json:"nodeId,omitempty"`

	Train   *TrainSpec `json:"train,omitempty"`
	Queries []Query    `json:"queries,omitempty"`

	Code        string       `json:"code,omitempty"`
	Error       string       `json:"error,omitempty"`
	TrainResult *TrainResult `json:"trainResult,omitempty"`
	Probs       [][]float64  `json:"probs,omitempty"`
	Trees       int          `json:"trees,omitempty"`
	Version     int64        `json:"version,omitempty"`
	Stats       *NodeStats   `json:"stats,omitempty"`
	Progress    *Progress    `json:"progress,omitempty"`
	Log         *LogLine     `json:"log,omitempty"`
}

// Progress informa el avance de un entrenamiento en un nodo.
type Progress struct {
	Version   int64   `json:"version"`
	Phase     string  `json:"phase"` // cargando | expandiendo | entrenando | listo
	Done      int     `json:"done"`
	Total     int     `json:"total"`
	Records   int64   `json:"records"`
	ElapsedMs float64 `json:"elapsedMs"`
}

// LogLine es una línea de log que el nodo envía al coordinador.
type LogLine struct {
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Text  string    `json:"text"`
}

// TrainSpec: qué shard entrena el nodo, con qué variables, qué años y qué
// árboles (ids globales) construye.
type TrainSpec struct {
	Version    int64         `json:"version"`
	Shard      int           `json:"shard"`
	Shards     int           `json:"shards"`
	TreeIDs    []int         `json:"treeIds"`
	Config     forest.Config `json:"config"`
	Workers    int           `json:"workers"`
	FeatureSet string        `json:"featureSet"`
	TrainFrom  int           `json:"trainFrom"` // primer año de entrenamiento (incluido)
	TrainTo    int           `json:"trainTo"`   // último año de entrenamiento (incluido)
	DataPath   string        `json:"dataPath,omitempty"`
}

type TrainResult struct {
	Version      int64     `json:"version" bson:"version"`
	Shard        int       `json:"shard" bson:"shard"`
	Shards       int       `json:"shards" bson:"shards"`
	Trees        int       `json:"trees" bson:"trees"`
	TreeIDs      []int     `json:"treeIds" bson:"treeIds"`
	TrainRecords int64     `json:"trainRecords" bson:"trainRecords"`
	ClassCounts  [6]int64  `json:"classCounts" bson:"classCounts"`
	Workers      int       `json:"workers" bson:"workers"`
	LoadMs       float64   `json:"loadMs" bson:"loadMs"`
	ExpandMs     float64   `json:"expandMs" bson:"expandMs"`
	TrainMs      float64   `json:"trainMs" bson:"trainMs"`
	TotalMs      float64   `json:"totalMs" bson:"totalMs"`
	TreeNodes    int       `json:"treeNodes" bson:"treeNodes"`
	Importance   []float64 `json:"importance" bson:"importance"`
	Signature    float64   `json:"signature" bson:"signature"`
	FeatureSet   string    `json:"featureSet" bson:"featureSet"`
	CachedMatrix bool      `json:"cachedMatrix" bson:"cachedMatrix"`
}

// Query es una consulta de predicción. Franja solo la usa el conjunto "pc4".
type Query struct {
	District string `json:"district"`
	Franja   int    `json:"franja,omitempty"`
	Mes      int    `json:"mes"`
	Anio     int    `json:"anio"`
}

// NodeStats viaja en cada PONG (alimenta /api/v1/metrics).
type NodeStats struct {
	Trained    bool    `json:"trained" bson:"trained"`
	Version    int64   `json:"version" bson:"version"`
	Trees      int     `json:"trees" bson:"trees"`
	Shard      int     `json:"shard" bson:"shard"`
	Records    int64   `json:"trainRecords" bson:"trainRecords"`
	Goroutines int     `json:"goroutines" bson:"goroutines"`
	HeapMB     float64 `json:"heapMB" bson:"heapMB"`
	NumCPU     int     `json:"numCPU" bson:"numCPU"`
	UptimeS    float64 `json:"uptimeS" bson:"uptimeS"`
	Requests   int64   `json:"requests" bson:"requests"`
	Busy       bool    `json:"training" bson:"training"`
	CPUPct     float64 `json:"cpuPct" bson:"cpuPct"` // % de un núcleo (100 = un núcleo completo)
	RSSMB      float64 `json:"rssMB" bson:"rssMB"`
	FeatureSet string  `json:"featureSet,omitempty" bson:"featureSet,omitempty"`
	Phase      string  `json:"phase,omitempty" bson:"phase,omitempty"`
	TreesDone  int     `json:"treesDone" bson:"treesDone"`
	TreesTotal int     `json:"treesTotal" bson:"treesTotal"`
}
