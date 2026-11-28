package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jotabe12/cc65-tb2-grupo3/internal/auth"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/logbuf"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/protocol"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/ws"
)

// Canales (topics) del WebSocket. metrics y logs son solo para administradores.
const (
	TopicMetrics    = "metrics"
	TopicTraining   = "training"
	TopicPrediction = "prediction"
	TopicLogs       = "logs"
)

var adminTopics = map[string]bool{TopicMetrics: true, TopicLogs: true}

// Event es el sobre JSON que recibe el frontend.
type Event struct {
	Type string    `json:"type"`
	Time time.Time `json:"time"`
	Data any       `json:"data"`
}

// hub difunde eventos a los clientes conectados (patrón publicador-suscriptor):
// cada cliente tiene un canal con búfer y una goroutine escritora; si un
// cliente lento llena su búfer, sus mensajes se descartan (no frena a nadie).
type hub struct {
	mu      sync.RWMutex
	clients map[*wsClient]struct{}
	count   atomic.Int64
	admins  atomic.Int64
	sent    atomic.Int64
	dropped atomic.Int64
	total   atomic.Int64
}

type wsClient struct {
	conn   *ws.Conn
	user   string
	role   string
	send   chan []byte
	topics map[string]bool
	mu     sync.RWMutex
	done   chan struct{}
	once   sync.Once
}

func newHub() *hub { return &hub{clients: map[*wsClient]struct{}{}} }

func (h *hub) add(c *wsClient) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	h.count.Add(1)
	h.total.Add(1)
	if c.role == auth.RoleAdmin {
		h.admins.Add(1)
	}
}

func (h *hub) remove(c *wsClient) {
	h.mu.Lock()
	_, ok := h.clients[c]
	delete(h.clients, c)
	h.mu.Unlock()
	if ok {
		h.count.Add(-1)
		if c.role == auth.RoleAdmin {
			h.admins.Add(-1)
		}
	}
}

func (h *hub) closeAll() {
	h.mu.Lock()
	list := make([]*wsClient, 0, len(h.clients))
	for c := range h.clients {
		list = append(list, c)
	}
	h.mu.Unlock()
	for _, c := range list {
		c.close(1001, "servidor detenido")
	}
}

func (c *wsClient) close(code uint16, reason string) {
	c.once.Do(func() {
		close(c.done)
		c.conn.Close(code, reason)
	})
}

func (c *wsClient) wants(topic string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.topics[topic]
}

// publish serializa una vez y entrega sin bloquear a los clientes suscritos.
func (h *hub) publish(topic string, data any) {
	if h.count.Load() == 0 || (adminTopics[topic] && h.admins.Load() == 0) {
		return
	}
	b, err := json.Marshal(Event{Type: topic, Time: time.Now().UTC(), Data: data})
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if !c.wants(topic) {
			continue
		}
		select {
		case c.send <- b:
			h.sent.Add(1)
		default:
			h.dropped.Add(1)
		}
	}
}

// handleWS: GET /ws. El token JWT viaja en el subprotocolo ("jwt", "<token>"),
// que el navegador sí permite fijar, o en ?token= (clientes de prueba).
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if o := r.Header.Get("Origin"); o != "" && !s.origins[o] {
		s.nDenied.Add(1)
		writeErr(w, http.StatusForbidden, "CORS_ORIGIN_DENIED", "origen no permitido para WebSocket: "+o, "")
		return
	}
	token, sub := r.URL.Query().Get("token"), ""
	if protos := ws.Subprotocols(r); len(protos) >= 2 && protos[0] == "jwt" {
		token, sub = protos[1], "jwt"
	}
	claims, err := s.cfg.Signer.Verify(token)
	if err != nil {
		s.nDenied.Add(1)
		writeErr(w, http.StatusUnauthorized, "INVALID_TOKEN", "WebSocket sin token válido: "+err.Error(), "")
		return
	}
	conn, err := ws.Upgrade(w, r, sub)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_WEBSOCKET", err.Error(), "")
		return
	}
	c := &wsClient{conn: conn, user: claims.Sub, role: claims.Role, send: make(chan []byte, 64),
		topics: map[string]bool{TopicTraining: true, TopicPrediction: true}, done: make(chan struct{})}
	if claims.Role == auth.RoleAdmin {
		c.topics[TopicMetrics], c.topics[TopicLogs] = true, true
	}
	s.hub.add(c)
	s.cfg.Logf("WebSocket abierto: %s (%s) desde %s", claims.Sub, claims.Role, s.clientIP(r))
	hello, _ := json.Marshal(Event{Type: "hello", Time: time.Now().UTC(), Data: map[string]any{
		"user": claims.Sub, "role": claims.Role, "topics": c.topicList(), "model": s.modelInfo()}})
	c.send <- hello

	go s.wsWriter(c)
	s.wsReader(c) // bloquea hasta que el cliente se desconecte
	s.hub.remove(c)
	c.close(1000, "")
	s.cfg.Logf("WebSocket cerrado: %s", claims.Sub)
}

func (c *wsClient) topicList() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.topics))
	for t, ok := range c.topics {
		if ok {
			out = append(out, t)
		}
	}
	return out
}

// wsWriter: única goroutine que escribe en el socket; envía un ping cada 25 s.
func (s *Server) wsWriter(c *wsClient) {
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-c.done:
			return
		case b := <-c.send:
			if err := c.conn.WriteText(b, 5*time.Second); err != nil {
				c.close(1011, "error de escritura")
				return
			}
		case <-ping.C:
			if err := c.conn.WriteMessage(ws.OpPing, nil, 5*time.Second); err != nil {
				c.close(1011, "error de escritura")
				return
			}
		}
	}
}

// wsReader lee los mensajes del cliente: {"subscribe":[...]} o {"unsubscribe":[...]}.
func (s *Server) wsReader(c *wsClient) {
	extend := func() { _ = c.conn.SetReadDeadline(time.Now().Add(70 * time.Second)) }
	extend()
	for {
		_, msg, err := c.conn.ReadMessage(extend)
		if err != nil {
			return
		}
		extend()
		var req struct {
			Subscribe   []string `json:"subscribe"`
			Unsubscribe []string `json:"unsubscribe"`
		}
		if json.Unmarshal(msg, &req) != nil {
			continue
		}
		c.mu.Lock()
		for _, t := range req.Subscribe {
			t = strings.ToLower(t)
			if adminTopics[t] && c.role != auth.RoleAdmin {
				continue // un analista no puede suscribirse a métricas ni logs
			}
			if t == TopicMetrics || t == TopicTraining || t == TopicPrediction || t == TopicLogs {
				c.topics[t] = true
			}
		}
		for _, t := range req.Unsubscribe {
			delete(c.topics, strings.ToLower(t))
		}
		c.mu.Unlock()
	}
}

// onNodeEvent recibe PROGRESS y LOG de los nodos (por TCP) y los reenvía.
func (s *Server) onNodeEvent(nodeID string, m protocol.Message) {
	switch m.Type {
	case protocol.TypeProgress:
		if m.Progress != nil {
			s.hub.publish(TopicTraining, map[string]any{"event": "progreso", "node": nodeID, "version": m.Progress.Version,
				"phase": m.Progress.Phase, "done": m.Progress.Done, "total": m.Progress.Total,
				"records": m.Progress.Records, "elapsedMs": m.Progress.ElapsedMs})
		}
	case protocol.TypeLog:
		if m.Log != nil {
			s.cfg.Logs.Add(nodeID, m.Log.Level, m.Log.Text)
		}
	}
}

// onLog difunde cada línea nueva del buffer (excepto los accesos HTTP, que son
// muchos; esos se consultan con GET /api/v1/admin/logs).
func (s *Server) onLog(e logbuf.Entry) {
	if e.Level != "acceso" {
		s.hub.publish(TopicLogs, e)
	}
}
