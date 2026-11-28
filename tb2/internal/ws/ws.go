// Package ws implementa el lado servidor de WebSocket (RFC 6455) con la
// biblioteca estándar: handshake HTTP/1.1 Upgrade, tramas de texto, ping/pong y
// cierre. Es suficiente para enviar eventos JSON en tiempo real al frontend.
package ws

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Opcodes (RFC 6455 §5.2).
const (
	OpContinuation = 0x0
	OpText         = 0x1
	OpBinary       = 0x2
	OpClose        = 0x8
	OpPing         = 0x9
	OpPong         = 0xA
)

// MaxMessage limita lo que acepta el servidor de un cliente (los clientes solo
// envían mensajes cortos de control).
const MaxMessage = 64 << 10

const guid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

var (
	ErrNotWebSocket = errors.New("la petición no es un handshake WebSocket válido")
	ErrTooLarge     = errors.New("mensaje WebSocket demasiado grande")
	ErrProtocol     = errors.New("error de protocolo WebSocket")
)

// AcceptKey calcula Sec-WebSocket-Accept a partir de Sec-WebSocket-Key.
func AcceptKey(key string) string {
	h := sha1.Sum([]byte(key + guid))
	return base64.StdEncoding.EncodeToString(h[:])
}

func headerHas(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for _, p := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(p), token) {
				return true
			}
		}
	}
	return false
}

// Subprotocols devuelve los subprotocolos que ofrece el cliente.
func Subprotocols(r *http.Request) []string {
	var out []string
	for _, v := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

// Conn es una conexión WebSocket del lado servidor. Las escrituras son seguras
// desde varias goroutines (mutex); las lecturas, desde una sola.
type Conn struct {
	conn net.Conn
	br   *bufio.Reader
	wmu  sync.Mutex
	once sync.Once
}

// Upgrade valida el handshake y toma la conexión (Hijack). subprotocol, si no
// está vacío, se devuelve en Sec-WebSocket-Protocol (debe estar entre los
// ofrecidos por el cliente).
func Upgrade(w http.ResponseWriter, r *http.Request, subprotocol string) (*Conn, error) {
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if r.Method != http.MethodGet || !headerHas(r.Header, "Connection", "upgrade") ||
		!headerHas(r.Header, "Upgrade", "websocket") || r.Header.Get("Sec-WebSocket-Version") != "13" {
		return nil, ErrNotWebSocket
	}
	if raw, err := base64.StdEncoding.DecodeString(key); err != nil || len(raw) != 16 {
		return nil, ErrNotWebSocket
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("el servidor HTTP no permite Hijack")
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	resp := "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + AcceptKey(key) + "\r\n"
	if subprotocol != "" {
		resp += "Sec-WebSocket-Protocol: " + subprotocol + "\r\n"
	}
	resp += "\r\n"
	_ = conn.SetDeadline(time.Time{})
	if _, err := rw.WriteString(resp); err != nil {
		conn.Close()
		return nil, err
	}
	if err := rw.Flush(); err != nil {
		conn.Close()
		return nil, err
	}
	return &Conn{conn: conn, br: rw.Reader}, nil
}

// WriteMessage envía una trama completa (FIN=1, sin máscara: el servidor no enmascara).
func (c *Conn) WriteMessage(op byte, data []byte, timeout time.Duration) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if timeout > 0 {
		_ = c.conn.SetWriteDeadline(time.Now().Add(timeout))
	}
	hdr := make([]byte, 0, 10)
	hdr = append(hdr, 0x80|op)
	switch n := len(data); {
	case n <= 125:
		hdr = append(hdr, byte(n))
	case n <= 0xFFFF:
		hdr = append(hdr, 126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	if _, err := c.conn.Write(hdr); err != nil {
		return err
	}
	_, err := c.conn.Write(data)
	return err
}

// WriteText envía un mensaje de texto (JSON).
func (c *Conn) WriteText(data []byte, timeout time.Duration) error {
	return c.WriteMessage(OpText, data, timeout)
}

// Close envía la trama de cierre (si se puede) y cierra el socket.
func (c *Conn) Close(code uint16, reason string) {
	c.once.Do(func() {
		p := binary.BigEndian.AppendUint16(nil, code)
		p = append(p, reason...)
		_ = c.WriteMessage(OpClose, p, time.Second)
		c.conn.Close()
	})
}

// SetReadDeadline fija el plazo de la próxima lectura.
func (c *Conn) SetReadDeadline(t time.Time) error { return c.conn.SetReadDeadline(t) }

// ReadMessage devuelve el siguiente mensaje de datos (texto o binario).
// Responde los ping con pong, ignora los pong (después de notificarlos con
// onPong) y ante un cierre responde el cierre y devuelve io.EOF.
func (c *Conn) ReadMessage(onPong func()) (byte, []byte, error) {
	var msg []byte
	var msgOp byte
	for {
		fin, op, payload, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch op {
		case OpPing:
			if err := c.WriteMessage(OpPong, payload, 5*time.Second); err != nil {
				return 0, nil, err
			}
			continue
		case OpPong:
			if onPong != nil {
				onPong()
			}
			continue
		case OpClose:
			code := uint16(1000)
			if len(payload) >= 2 {
				code = binary.BigEndian.Uint16(payload)
			}
			c.Close(code, "")
			return 0, nil, io.EOF
		case OpText, OpBinary:
			if msg != nil {
				return 0, nil, ErrProtocol // nuevo mensaje sin terminar el anterior
			}
			msgOp = op
			msg = append([]byte{}, payload...)
		case OpContinuation:
			if msg == nil {
				return 0, nil, ErrProtocol
			}
			msg = append(msg, payload...)
		default:
			return 0, nil, ErrProtocol
		}
		if len(msg) > MaxMessage {
			return 0, nil, ErrTooLarge
		}
		if fin {
			return msgOp, msg, nil
		}
	}
}

// readFrame lee una trama del cliente; el RFC exige que venga enmascarada.
func (c *Conn) readFrame() (bool, byte, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(c.br, h[:]); err != nil {
		return false, 0, nil, err
	}
	fin := h[0]&0x80 != 0
	if h[0]&0x70 != 0 {
		return false, 0, nil, ErrProtocol // RSV sin extensiones negociadas
	}
	op := h[0] & 0x0F
	masked := h[1]&0x80 != 0
	n := uint64(h[1] & 0x7F)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(c.br, b[:]); err != nil {
			return false, 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(c.br, b[:]); err != nil {
			return false, 0, nil, err
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if !masked {
		return false, 0, nil, fmt.Errorf("%w: trama del cliente sin máscara", ErrProtocol)
	}
	if op >= 0x8 && (n > 125 || !fin) {
		return false, 0, nil, ErrProtocol // las tramas de control son cortas y no se fragmentan
	}
	if n > MaxMessage {
		return false, 0, nil, ErrTooLarge
	}
	var mask [4]byte
	if _, err := io.ReadFull(c.br, mask[:]); err != nil {
		return false, 0, nil, err
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return fin, op, payload, nil
}
