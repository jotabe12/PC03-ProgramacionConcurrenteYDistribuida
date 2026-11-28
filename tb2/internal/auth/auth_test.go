package auth

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestPasswordHashing(t *testing.T) {
	ph, err := HashPassword("Clave-Segura-2026")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword("Clave-Segura-2026", ph) {
		t.Fatal("la contraseña correcta no se acepta")
	}
	if CheckPassword("clave-segura-2026", ph) || CheckPassword("", ph) {
		t.Fatal("se aceptó una contraseña incorrecta")
	}
	ph2, _ := HashPassword("Clave-Segura-2026")
	if ph.Salt == ph2.Salt || ph.Hash == ph2.Hash {
		t.Fatal("la sal debe ser aleatoria")
	}
}

func TestJWT(t *testing.T) {
	if _, err := NewSigner([]byte("corta"), time.Hour); err == nil {
		t.Fatal("se aceptó una clave de menos de 32 caracteres")
	}
	s, _ := NewSigner([]byte(strings.Repeat("k", 40)), time.Hour)
	tok, c, err := s.Sign("admin", RoleAdmin)
	if err != nil || c.Exp-c.Iat != 3600 {
		t.Fatalf("firma: %v %+v", err, c)
	}
	got, err := s.Verify(tok)
	if err != nil || got.Sub != "admin" || got.Role != RoleAdmin {
		t.Fatalf("verificación: %v %+v", err, got)
	}
	parts := strings.Split(tok, ".")

	// Firma de otra clave.
	other, _ := NewSigner([]byte(strings.Repeat("x", 40)), time.Hour)
	if _, err := other.Verify(tok); err != ErrTokenSignature {
		t.Fatalf("clave distinta: %v", err)
	}
	// Payload alterado (escalar a admin un token de analista).
	ta, _, _ := s.Sign("ana", RoleAnalista)
	pa := strings.Split(ta, ".")
	forged := pa[0] + "." + b64.EncodeToString([]byte(`{"sub":"ana","role":"admin","iss":"cc65-tb2-grupo3","iat":1,"exp":9999999999}`)) + "." + pa[2]
	if _, err := s.Verify(forged); err != ErrTokenSignature {
		t.Fatalf("payload alterado: %v", err)
	}
	// alg = none.
	none := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`)) + "." + parts[1] + "."
	if _, err := s.Verify(none); err != ErrTokenAlg {
		t.Fatalf("alg none: %v", err)
	}
	// Formato.
	if _, err := s.Verify("abc"); err != ErrTokenFormat {
		t.Fatalf("formato: %v", err)
	}
	// Expiración.
	s.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := s.Verify(tok); err != ErrTokenExpired {
		t.Fatalf("expiración: %v", err)
	}
}
