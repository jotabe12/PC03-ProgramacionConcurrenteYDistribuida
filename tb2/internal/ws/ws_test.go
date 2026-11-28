package ws

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Ejemplo del RFC 6455 §1.3.
func TestAcceptKeyRFC(t *testing.T) {
	if got := AcceptKey("dGhlIHNhbXBsZSBub25jZQ=="); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("AcceptKey = %s", got)
	}
}

// clientFrame arma una trama enmascarada como la enviaría un navegador.
func clientFrame(op byte, payload []byte, fin bool) []byte {
	b0 := op
	if fin {
		b0 |= 0x80
	}
	f := []byte{b0}
	switch n := len(payload); {
	case n <= 125:
		f = append(f, 0x80|byte(n))
	default:
		f = append(f, 0x80|126, byte(n>>8), byte(n))
	}
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	f = append(f, mask[:]...)
	for i, c := range payload {
		f = append(f, c^mask[i%4])
	}
	return f
}

func readServerFrame(t *testing.T, br *bufio.Reader) (byte, []byte) {
	t.Helper()
	var h [2]byte
	if _, err := io.ReadFull(br, h[:]); err != nil {
		t.Fatal(err)
	}
	n := int(h[1] & 0x7F)
	if n == 126 {
		var b [2]byte
		io.ReadFull(br, b[:])
		n = int(binary.BigEndian.Uint16(b[:]))
	}
	p := make([]byte, n)
	io.ReadFull(br, p)
	return h[0] & 0x0F, p
}

func TestEchoPingAndClose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Upgrade(w, r, "jwt")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		for {
			_, msg, err := c.ReadMessage(nil)
			if err != nil {
				return
			}
			_ = c.WriteText(append([]byte("eco:"), msg...), time.Second)
		}
	}))
	defer srv.Close()

	// Handshake inválido -> 400.
	resp, _ := http.Get(srv.URL)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("GET normal: %d", resp.StatusCode)
	}

	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	req := "GET /ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: keep-alive, Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: jwt, abc.def.ghi\r\n\r\n"
	conn.Write([]byte(req))
	br := bufio.NewReader(conn)
	resp, err = http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 101 || resp.Header.Get("Sec-WebSocket-Accept") != AcceptKey(key) ||
		resp.Header.Get("Sec-WebSocket-Protocol") != "jwt" {
		t.Fatalf("handshake: %v %+v", err, resp)
	}
	// Mensaje fragmentado en dos tramas (texto + continuación).
	conn.Write(clientFrame(OpText, []byte("hola "), false))
	conn.Write(clientFrame(OpContinuation, []byte("mundo"), true))
	if op, p := readServerFrame(t, br); op != OpText || string(p) != "eco:hola mundo" {
		t.Fatalf("eco: %d %q", op, p)
	}
	// Mensaje de 300 bytes (longitud extendida de 16 bits).
	big := []byte(strings.Repeat("x", 300))
	conn.Write(clientFrame(OpText, big, true))
	if _, p := readServerFrame(t, br); len(p) != 304 {
		t.Fatalf("eco largo de %d bytes", len(p))
	}
	// Ping -> Pong con el mismo contenido.
	conn.Write(clientFrame(OpPing, []byte("p1"), true))
	if op, p := readServerFrame(t, br); op != OpPong || string(p) != "p1" {
		t.Fatalf("pong: %d %q", op, p)
	}
	// Cierre -> el servidor responde Close.
	conn.Write(clientFrame(OpClose, []byte{0x03, 0xE8}, true))
	if op, _ := readServerFrame(t, br); op != OpClose {
		t.Fatalf("se esperaba Close, llegó %d", op)
	}
}

func TestRejectsUnmaskedFrames(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	c := &Conn{conn: b, br: bufio.NewReader(b)}
	go a.Write([]byte{0x81, 0x02, 'h', 'i'}) // sin máscara
	if _, _, err := c.ReadMessage(nil); err == nil {
		t.Fatal("se aceptó una trama de cliente sin máscara")
	}
}
