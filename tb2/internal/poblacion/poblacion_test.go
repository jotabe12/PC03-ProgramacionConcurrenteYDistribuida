package poblacion

import (
	"os"
	"testing"
)

const xlsx = "../../data/6894980-peru-poblacion-total-proyectada-al-30-de-junio-de-cada-ano-segun-departamento-provincia-y-distrito-2018-2026.xlsx"

func TestLoadINEI(t *testing.T) {
	if _, err := os.Stat(xlsx); err != nil {
		t.Skip("XLSX del INEI no disponible")
	}
	tab, err := Load(xlsx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tab.Years) != 9 || tab.Years[0] != 2018 || tab.Years[8] != 2026 {
		t.Fatalf("años inesperados: %v", tab.Years)
	}
	if len(tab.ByUbigeo) != 1892 {
		t.Fatalf("se esperaban 1892 distritos, hay %d", len(tab.ByUbigeo))
	}
	// Chachapoyas (010101): 36,602 en 2018 y 42,342 en 2026 (valores del cuadro del INEI).
	if p, _ := tab.Get("10101", 2018); p != 36602 {
		t.Fatalf("Chachapoyas 2018 = %d", p)
	}
	if p, _ := tab.Get("010101", 2030); p != 42342 {
		t.Fatalf("Chachapoyas 2030 (se usa 2026) = %d", p)
	}
	// San Juan de Lurigancho (150132) debe existir y superar el millón de habitantes.
	if p, ok := tab.Get("150132", 2026); !ok || p < 1000000 {
		t.Fatalf("San Juan de Lurigancho 2026 = %d (%v)", p, ok)
	}
	if tab.Partial != 18 {
		t.Fatalf("se esperaban 18 distritos creados después de 2018, hay %d", tab.Partial)
	}
	// Alto Trujillo (130112) se creó en 2022: para 2019 se usa el primer año publicado (2023).
	if p, ok := tab.Get("130112", 2019); !ok || p != 84323 {
		t.Fatalf("Alto Trujillo 2019 = %d (%v)", p, ok)
	}
	if _, ok := tab.Get("150100", 2026); ok {
		t.Fatal("las filas de totales (terminan en 00) no deben cargarse")
	}
}

func TestSplitRef(t *testing.T) {
	c, r := splitRef("AB123")
	if c != "AB" || r != 123 {
		t.Fatalf("%s %d", c, r)
	}
}
