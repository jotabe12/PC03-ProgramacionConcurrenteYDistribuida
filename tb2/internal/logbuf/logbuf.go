// Package logbuf guarda las últimas líneas de log del coordinador y de los
// nodos en un buffer circular (para GET /api/v1/admin/logs y el WebSocket).
package logbuf

import (
	"strings"
	"sync"
	"time"
)

// Entry es una línea de log.
type Entry struct {
	Seq    int64     `json:"seq"`
	Time   time.Time `json:"time"`
	Source string    `json:"source"` // coordinator | ml-node-1 | ...
	Level  string    `json:"level"`  // info | warn | error | acceso
	Text   string    `json:"text"`
}

// circ es un buffer circular de tamaño fijo (no es seguro por sí solo).
type circ struct {
	buf  []Entry
	next int
	full bool
}

func (c *circ) add(e Entry) {
	c.buf[c.next] = e
	c.next = (c.next + 1) % len(c.buf)
	if c.next == 0 {
		c.full = true
	}
}

func (c *circ) last(n int, level, source string, out []Entry) []Entry {
	size := c.next
	if c.full {
		size = len(c.buf)
	}
	for i := 0; i < size && len(out) < n; i++ {
		e := c.buf[(c.next-1-i+len(c.buf))%len(c.buf)]
		if (level == "" || e.Level == level) && (source == "" || e.Source == source) {
			out = append(out, e)
		}
	}
	return out
}

// Ring guarda las últimas líneas protegidas con un mutex. Los accesos HTTP van
// a un buffer aparte para que una prueba de carga no desplace los eventos
// importantes (entrenamientos, nodos, errores).
type Ring struct {
	mu     sync.Mutex
	events circ
	access circ
	seq    int64
	OnAdd  func(Entry) // opcional: se llama fuera del candado
}

func New(size int) *Ring {
	if size < 1 {
		size = 1
	}
	return &Ring{events: circ{buf: make([]Entry, size)}, access: circ{buf: make([]Entry, max(size/2, 1))}}
}

// Add agrega una línea y devuelve la entrada con su número de secuencia.
func (r *Ring) Add(source, level, text string) Entry {
	r.mu.Lock()
	r.seq++
	e := Entry{Seq: r.seq, Time: time.Now().UTC(), Source: source, Level: level, Text: strings.TrimRight(text, "\r\n")}
	if level == "acceso" {
		r.access.add(e)
	} else {
		r.events.add(e)
	}
	cb := r.OnAdd
	r.mu.Unlock()
	if cb != nil {
		cb(e)
	}
	return e
}

// Last devuelve hasta n entradas (las más recientes al final), filtradas por
// nivel y fuente. level "acceso" consulta los accesos HTTP; "" todos los demás.
func (r *Ring) Last(n int, level, source string) []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Entry, 0, min(n, 1024))
	if level == "acceso" {
		out = r.access.last(n, "", source, out)
	} else {
		out = r.events.last(n, level, source, out)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Classify asigna un nivel a una línea de log del coordinador.
func Classify(text string) string {
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, " -> ") && (strings.HasPrefix(t, "get ") || strings.HasPrefix(t, "post ") ||
		strings.HasPrefix(t, "options ") || strings.HasPrefix(t, "put ") || strings.HasPrefix(t, "delete ")):
		return "acceso"
	case strings.Contains(t, "error") || strings.Contains(t, "fallid") || strings.Contains(t, "no pudo") ||
		strings.Contains(t, "panic"):
		return "error"
	case strings.Contains(t, "desconect") || strings.Contains(t, "tardía") || strings.Contains(t, "reentren") ||
		strings.Contains(t, "no disponible") || strings.Contains(t, "rechaz") || strings.Contains(t, "denegad"):
		return "warn"
	}
	return "info"
}

// Writer adapta el Ring como destino de log.Logger (una línea por Write).
type Writer struct {
	Ring   *Ring
	Source string
}

func (w Writer) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\r\n")
	// El prefijo de fecha de log.LstdFlags ya va en Entry.Time: se quita si existe
	// ("2026/11/28 10:00:00.000000 mensaje").
	if len(line) > 27 && line[4] == '/' && line[7] == '/' && line[10] == ' ' {
		if i := strings.IndexByte(line[11:], ' '); i >= 0 {
			line = line[11+i+1:]
		}
	}
	w.Ring.Add(w.Source, Classify(line), line)
	return len(p), nil
}
