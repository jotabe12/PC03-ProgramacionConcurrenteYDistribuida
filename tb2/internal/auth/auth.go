// Package auth implementa la autenticación de la API con la biblioteca estándar:
// contraseñas con PBKDF2-HMAC-SHA256 (crypto/pbkdf2, Go 1.24) y tokens JWT
// firmados con HS256 (RFC 7519 / RFC 7515), sin dependencias externas.
package auth

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Roles de la aplicación.
const (
	RoleAdmin    = "admin"    // entrena, ve métricas del clúster y logs
	RoleAnalista = "analista" // consulta predicciones, patrullaje e impacto
)

// ---------------------------------------------------------------- contraseñas

// PBKDF2Iterations: 210,000 iteraciones con SHA-256 (recomendación OWASP 2023).
const PBKDF2Iterations = 210000

const saltLen, keyLen = 16, 32

// PasswordHash es lo que se guarda en la colección users (nunca la contraseña).
type PasswordHash struct {
	Salt       string `json:"salt" bson:"salt"`             // base64
	Hash       string `json:"hash" bson:"hash"`             // base64
	Iterations int    `json:"iterations" bson:"iterations"` // PBKDF2
}

// HashPassword deriva la clave con una sal aleatoria.
func HashPassword(password string) (PasswordHash, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return PasswordHash{}, err
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, PBKDF2Iterations, keyLen)
	if err != nil {
		return PasswordHash{}, err
	}
	return PasswordHash{Salt: base64.StdEncoding.EncodeToString(salt), Hash: base64.StdEncoding.EncodeToString(key),
		Iterations: PBKDF2Iterations}, nil
}

// CheckPassword compara en tiempo constante.
func CheckPassword(password string, ph PasswordHash) bool {
	salt, err1 := base64.StdEncoding.DecodeString(ph.Salt)
	want, err2 := base64.StdEncoding.DecodeString(ph.Hash)
	if err1 != nil || err2 != nil || ph.Iterations < 1 || len(want) == 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, password, salt, ph.Iterations, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ---------------------------------------------------------------- JWT HS256

// Claims del token.
type Claims struct {
	Sub  string `json:"sub"`  // usuario
	Role string `json:"role"` // admin | analista
	Iss  string `json:"iss"`
	Iat  int64  `json:"iat"`
	Exp  int64  `json:"exp"`
	Jti  string `json:"jti"`
}

// Issuer identifica a esta API en el campo iss.
const Issuer = "cc65-tb2-grupo3"

var (
	ErrTokenFormat    = errors.New("token con formato inválido")
	ErrTokenAlg       = errors.New("algoritmo de firma no permitido")
	ErrTokenSignature = errors.New("firma del token inválida")
	ErrTokenExpired   = errors.New("token expirado")
	ErrTokenClaims    = errors.New("token con datos inválidos")
)

// Signer firma y verifica tokens con una clave secreta compartida.
type Signer struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

// NewSigner exige una clave de al menos 32 bytes (256 bits, como HS256).
func NewSigner(secret []byte, ttl time.Duration) (*Signer, error) {
	if len(secret) < 32 {
		return nil, fmt.Errorf("JWT_SECRET debe tener al menos 32 caracteres (tiene %d)", len(secret))
	}
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	return &Signer{secret: append([]byte(nil), secret...), ttl: ttl, now: time.Now}, nil
}

// RandomSecret genera una clave aleatoria (si no se configuró JWT_SECRET).
func RandomSecret() []byte {
	b := make([]byte, 48)
	_, _ = rand.Read(b)
	return []byte(base64.RawURLEncoding.EncodeToString(b))
}

func (s *Signer) TTL() time.Duration { return s.ttl }

var b64 = base64.RawURLEncoding

// header fijo: la verificación exige exactamente este alg (evita "alg": "none").
var headerSeg = b64.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))

// Sign emite un token para el usuario y rol dados.
func (s *Signer) Sign(user, role string) (string, Claims, error) {
	now := s.now()
	jti := make([]byte, 9)
	_, _ = rand.Read(jti)
	c := Claims{Sub: user, Role: role, Iss: Issuer, Iat: now.Unix(), Exp: now.Add(s.ttl).Unix(), Jti: b64.EncodeToString(jti)}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", Claims{}, err
	}
	signing := headerSeg + "." + b64.EncodeToString(payload)
	return signing + "." + b64.EncodeToString(s.mac(signing)), c, nil
}

func (s *Signer) mac(signing string) []byte {
	m := hmac.New(sha256.New, s.secret)
	m.Write([]byte(signing))
	return m.Sum(nil)
}

// Verify valida formato, algoritmo, firma (en tiempo constante), emisor y vigencia.
func (s *Signer) Verify(token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, ErrTokenFormat
	}
	hdr, err := b64.DecodeString(parts[0])
	if err != nil {
		return Claims{}, ErrTokenFormat
	}
	var h struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if json.Unmarshal(hdr, &h) != nil {
		return Claims{}, ErrTokenFormat
	}
	if h.Alg != "HS256" {
		return Claims{}, ErrTokenAlg
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil {
		return Claims{}, ErrTokenFormat
	}
	if !hmac.Equal(sig, s.mac(parts[0]+"."+parts[1])) {
		return Claims{}, ErrTokenSignature
	}
	payload, err := b64.DecodeString(parts[1])
	if err != nil {
		return Claims{}, ErrTokenFormat
	}
	var c Claims
	if json.Unmarshal(payload, &c) != nil {
		return Claims{}, ErrTokenFormat
	}
	if c.Iss != Issuer || c.Sub == "" || (c.Role != RoleAdmin && c.Role != RoleAnalista) {
		return Claims{}, ErrTokenClaims
	}
	if s.now().Unix() >= c.Exp {
		return Claims{}, ErrTokenExpired
	}
	return c, nil
}
