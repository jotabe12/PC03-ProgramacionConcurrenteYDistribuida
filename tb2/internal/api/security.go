package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jotabe12/cc65-tb2-grupo3/internal/auth"
	"github.com/jotabe12/cc65-tb2-grupo3/internal/storage"
)

// Políticas de seguridad de la API:
//  1. Autenticación con JWT HS256 (Authorization: Bearer <token>).
//  2. Autorización por rol: admin (entrenar, métricas, logs) y analista (consultas).
//  3. CORS con lista blanca de orígenes (también se valida el Origin del WebSocket).
//  4. Límite de intentos de login fallidos por IP (fuerza bruta) y cuota por usuario analista.
//  5. Cuerpos JSON acotados (http.MaxBytesReader) y cabeceras de seguridad.

type ctxKey int

const claimsKey ctxKey = 1

func claimsOf(r *http.Request) auth.Claims {
	c, _ := r.Context().Value(claimsKey).(auth.Claims)
	return c
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

// authed exige un token válido con alguno de los roles indicados y aplica la
// cuota por usuario (solo analistas).
func (s *Server) authed(roles []string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
		if tok == "" {
			s.nDenied.Add(1)
			w.Header().Set("WWW-Authenticate", `Bearer realm="tb2"`)
			writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "falta el token (Authorization: Bearer <token>)", "POST /api/v1/auth/login")
			return
		}
		c, err := s.cfg.Signer.Verify(tok)
		if err != nil {
			s.nDenied.Add(1)
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			writeErr(w, http.StatusUnauthorized, "INVALID_TOKEN", "token inválido: "+err.Error(), "vuelva a iniciar sesión")
			return
		}
		allowed := false
		for _, role := range roles {
			if c.Role == role {
				allowed = true
				break
			}
		}
		if !allowed {
			s.nDenied.Add(1)
			writeErr(w, http.StatusForbidden, "FORBIDDEN", fmt.Sprintf("el rol %s no tiene permiso para esta operación", c.Role),
				"requiere el rol "+strings.Join(roles, " o "))
			return
		}
		if c.Role == auth.RoleAnalista {
			if ok, retry := s.quotas.allow(c.Sub); !ok {
				s.nLimited.Add(1)
				w.Header().Set("Retry-After", strconv.Itoa(retry))
				writeErr(w, http.StatusTooManyRequests, "QUOTA_EXCEEDED",
					fmt.Sprintf("cuota de %d peticiones por minuto superada", s.cfg.AnalystRPM), "espere unos segundos")
				return
			}
		}
		h(w, r.WithContext(context.WithValue(r.Context(), claimsKey, c)))
	})
}

// cors aplica la lista blanca de orígenes. Las peticiones sin Origin (Postman,
// PowerShell, curl) no son de navegador y siguen la autenticación normal.
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Origin")
		if !s.origins[origin] {
			s.nDenied.Add(1)
			writeErr(w, http.StatusForbidden, "CORS_ORIGIN_DENIED", "origen no permitido por la política CORS: "+origin,
				"agregue el origen a CORS_ORIGINS")
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Access-Control-Expose-Headers", "X-Response-Time-Ms, Retry-After")
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			h.Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP: detrás de nginx (TrustProxy) se usa X-Real-IP; si no, la IP del socket.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---------------------------------------------------------------- usuarios y login

func (s *Server) seedUser(ctx context.Context, u SeedUser) error {
	if u.Username == "" || u.Password == "" {
		return nil
	}
	if u.Role != auth.RoleAdmin && u.Role != auth.RoleAnalista {
		return fmt.Errorf("rol inválido para %s: %s", u.Username, u.Role)
	}
	if len(u.Password) < 8 {
		return fmt.Errorf("la contraseña de %s debe tener al menos 8 caracteres", u.Username)
	}
	// Si el usuario ya existe con la misma contraseña, no se vuelve a derivar.
	if old, err := s.store.GetUser(ctx, u.Username); err == nil && old.Role == u.Role &&
		auth.CheckPassword(u.Password, auth.PasswordHash{Salt: old.Salt, Hash: old.Hash, Iterations: old.Iterations}) {
		return nil
	}
	ph, err := auth.HashPassword(u.Password)
	if err != nil {
		return err
	}
	if err := s.store.UpsertUser(ctx, storage.User{Username: u.Username, Role: u.Role, Salt: ph.Salt, Hash: ph.Hash,
		Iterations: ph.Iterations, UpdatedAt: time.Now().UTC()}); err != nil {
		return fmt.Errorf("guardando el usuario %s: %w", u.Username, err)
	}
	s.cfg.Logf("usuario %s (%s) registrado con PBKDF2-SHA256 (%d iteraciones)", u.Username, u.Role, ph.Iterations)
	return nil
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if ok, retry := s.logins.allowed(ip); !ok {
		s.nLimited.Add(1)
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		writeErr(w, http.StatusTooManyRequests, "TOO_MANY_ATTEMPTS",
			fmt.Sprintf("demasiados intentos fallidos desde %s; espere %d s", ip, retry), "")
		return
	}
	var req LoginRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Username == "" || req.Password == "" {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "se esperaba {\"username\": \"...\", \"password\": \"...\"}", "")
		return
	}
	u, err := s.store.GetUser(r.Context(), req.Username)
	if err != nil && !errors.Is(err, storage.ErrUserNotFound) {
		writeErr(w, http.StatusServiceUnavailable, "STORAGE_ERROR", "no se pudo consultar el usuario", "")
		return
	}
	// Mismo mensaje para usuario inexistente o contraseña incorrecta (no revela qué usuarios existen).
	if err != nil || !auth.CheckPassword(req.Password, auth.PasswordHash{Salt: u.Salt, Hash: u.Hash, Iterations: u.Iterations}) {
		s.logins.fail(ip)
		s.nDenied.Add(1)
		s.cfg.Logf("login rechazado para %q desde %s", req.Username, ip)
		writeErr(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "usuario o contraseña incorrectos", "")
		return
	}
	s.logins.reset(ip)
	tok, c, err := s.cfg.Signer.Sign(u.Username, u.Role)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "TOKEN_ERROR", err.Error(), "")
		return
	}
	s.cfg.Logf("login correcto de %s (%s) desde %s", u.Username, u.Role, ip)
	writeJSON(w, http.StatusOK, LoginResponse{Token: tok, TokenType: "Bearer", ExpiresAt: time.Unix(c.Exp, 0).UTC(),
		User: UserInfo{Username: u.Username, Role: u.Role}})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	c := claimsOf(r)
	writeJSON(w, http.StatusOK, map[string]any{"username": c.Sub, "role": c.Role,
		"issuedAt": time.Unix(c.Iat, 0).UTC(), "expiresAt": time.Unix(c.Exp, 0).UTC()})
}

// ---------------------------------------------------------------- limitadores

// ipLimiter cuenta intentos fallidos por IP en una ventana fija.
type ipLimiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	m      map[string]*ipWindow
}

type ipWindow struct {
	start time.Time
	fails int
}

func newIPLimiter(max int, window time.Duration) *ipLimiter {
	return &ipLimiter{max: max, window: window, m: map[string]*ipWindow{}}
}

func (l *ipLimiter) allowed(ip string) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.m[ip]
	if !ok || time.Since(e.start) >= l.window {
		return true, 0
	}
	if e.fails >= l.max {
		return false, int(math.Ceil((l.window - time.Since(e.start)).Seconds()))
	}
	return true, 0
}

func (l *ipLimiter) fail(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.m) > 10000 { // limpieza simple
		for k, e := range l.m {
			if time.Since(e.start) >= l.window {
				delete(l.m, k)
			}
		}
	}
	e, ok := l.m[ip]
	if !ok || time.Since(e.start) >= l.window {
		e = &ipWindow{start: time.Now()}
		l.m[ip] = e
	}
	e.fails++
}

func (l *ipLimiter) reset(ip string) {
	l.mu.Lock()
	delete(l.m, ip)
	l.mu.Unlock()
}

// userQuota es un token bucket por usuario: capacidad = rpm, recarga rpm/60 por segundo.
type userQuota struct {
	mu  sync.Mutex
	rpm int
	b   map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newUserQuota(rpm int) *userQuota { return &userQuota{rpm: rpm, b: map[string]*bucket{}} }

func (q *userQuota) allow(user string) (bool, int) {
	if q.rpm <= 0 {
		return true, 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	b, ok := q.b[user]
	if !ok {
		b = &bucket{tokens: float64(q.rpm), last: now}
		q.b[user] = b
	}
	rate := float64(q.rpm) / 60
	b.tokens = math.Min(float64(q.rpm), b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
	if b.tokens < 1 {
		return false, int(math.Ceil((1 - b.tokens) / rate))
	}
	b.tokens--
	return true, 0
}
