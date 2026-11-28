package dataset

import (
	"os"
	"testing"
)

const csvPath = "../../data/DATASET_Denuncias Policiales - Enero 2018 a Septiembre 2026.csv"

func loadOrSkip(t *testing.T, workers int) *Table {
	t.Helper()
	if _, err := os.Stat(csvPath); err != nil {
		t.Skip("CSV no disponible")
	}
	tab, err := Load(csvPath, workers)
	if err != nil {
		t.Fatal(err)
	}
	return tab
}

func TestNormalizeAndClasses(t *testing.T) {
	cases := map[string]int{"Extorsión": 4, "EXTORSIÓN": 4, "  hurto ": 1, "Violencia contra la mujer e integrantes": 0, "Otros": -1, "xyz": -2}
	for in, want := range cases {
		if got := ClassOf(Normalize(in)); got != want {
			t.Errorf("ClassOf(%q) = %d, se esperaba %d", in, got, want)
		}
	}
	if Normalize("Breña") != "BREÑA" {
		t.Errorf("la Ñ debe conservarse")
	}
	if ZoneOf("PROV. CONST. DEL CALLAO", "VENTANILLA") != 4 || ZoneOf("LIMA METROPOLITANA", "LURIGANCHO - CHOSICA") != 2 ||
		ZoneOf("LIMA METROPOLITANA", "PUCUSANA") != 3 {
		t.Errorf("zonas incorrectas")
	}
}

// Los totales deben coincidir con los verificados en la PC3.
func TestTotalsMatchPC3(t *testing.T) {
	tab := loadOrSkip(t, 4)
	s := tab.Stats
	if s.RowsRead != 376715 || s.DenunciasNacional != 7509690 || s.RowsValid != 25849 || s.DenunciasValidas != 1987556 {
		t.Fatalf("totales distintos a la PC3: %+v", s)
	}
	cat, err := BuildCatalog(tab)
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Districts) != 50 {
		t.Fatalf("se esperaban 50 distritos, hay %d", len(cat.Districts))
	}
	tot := ClassTotals(tab, nil)
	if tot[4] != 50677 {
		t.Fatalf("extorsión debe tener 50,677 denuncias en Lima y Callao, tiene %d", tot[4])
	}
	t.Logf("filas=%d válidas=%d denuncias=%d clases=%v carga=%.1f ms", s.RowsRead, s.RowsValid, s.DenunciasValidas, tot, s.LoadMs)
}

// El resultado de la carga concurrente no depende del número de workers.
func TestLoadDeterministic(t *testing.T) {
	a := loadOrSkip(t, 1)
	b := loadOrSkip(t, 8)
	if len(a.Rows) != len(b.Rows) {
		t.Fatal("distinto número de filas")
	}
	for i := range a.Rows {
		if a.Rows[i] != b.Rows[i] {
			t.Fatalf("fila %d distinta con 1 y 8 workers", i)
		}
	}
}

// Con N = 1, 2 y 3 shards: disjuntos, suman el total y cada uno tiene ~1/N de
// cada clase y de cada distrito (sharding estratificado).
func TestShardsStratified(t *testing.T) {
	tab := loadOrSkip(t, 0)
	cat, _ := BuildCatalog(tab)
	h := BuildHistory(tab, cat)
	full := Expand(tab, cat, h, FSTB2, 0, 1, nil)
	if full.Len() != 1987556 {
		t.Fatalf("expansión total %d", full.Len())
	}
	for _, shards := range []int{2, 3} {
		var sum int
		for s := 0; s < shards; s++ {
			m := Expand(tab, cat, h, FSTB2, s, shards, nil)
			sum += m.Len()
			want := 1 / float64(shards)
			share := float64(m.Len()) / float64(full.Len())
			if share < want-0.001 || share > want+0.001 {
				t.Errorf("%d shards: shard %d tiene %.4f del total", shards, s, share)
			}
			for k := 0; k < NumClasses; k++ {
				r := float64(m.Count[k]) / float64(full.Count[k])
				if r < want-0.004 || r > want+0.004 {
					t.Errorf("%d shards: shard %d clase %d: %.4f del total de la clase", shards, s, k, r)
				}
			}
			seen := map[uint8]bool{}
			for _, d := range m.X[0] {
				seen[d] = true
			}
			if len(seen) != 50 {
				t.Errorf("%d shards: shard %d solo tiene %d distritos", shards, s, len(seen))
			}
		}
		if sum != full.Len() {
			t.Fatalf("%d shards suman %d, se esperaban %d", shards, sum, full.Len())
		}
	}
	// El conjunto pc4 debe reproducir la franja imputada de la PC3 (Tabla 18) y
	// los mismos 1,760 vectores de prueba de la PC4.
	pc4 := Expand(tab, cat, h, FSPC4, 0, 1, nil)
	var byClass [NumClasses][NumFranjas]float64
	for i := range pc4.Y {
		byClass[pc4.Y[i]][pc4.X[2][i]]++
	}
	for k := 0; k < NumClasses; k++ {
		n := float64(pc4.Count[k])
		for f := 0; f < NumFranjas; f++ {
			if d := byClass[k][f]/n - priorFranja[k][f]; k != 5 && (d > 0.003 || d < -0.003) {
				t.Errorf("franja clase %d franja %d se aleja del supuesto: %.4f", k, f, d)
			}
		}
	}
	for _, fs := range []FeatureSet{FSPC4, FSTB2} {
		test := AggregateYear(tab, cat, fs, TestYear)
		var n int64
		for _, c := range test {
			for _, v := range c.Counts {
				n += v
			}
		}
		if n != 164221 {
			t.Fatalf("%s: prueba 2026 con %d denuncias, la PC3 reporta 164,221", fs, n)
		}
		if fs == FSPC4 && len(test) != 1760 {
			t.Fatalf("pc4: %d vectores de prueba, la PC4 tenía 1,760", len(test))
		}
		t.Logf("%s: prueba 2026 con %d denuncias en %d vectores distintos", fs, n, len(test))
	}
}

// La ventana del patrón reciente usa solo meses anteriores al mes objetivo.
func TestHistoryUsesOnlyPast(t *testing.T) {
	tab := loadOrSkip(t, 0)
	cat, _ := BuildCatalog(tab)
	h := BuildHistory(tab, cat)
	if h.Last != MonthIndex(2026, 9) {
		t.Fatalf("último mes con datos: %d", h.Last)
	}
	s, e := h.Window(MonthIndex(2026, 3))
	if s != MonthIndex(2025, 3) || e != MonthIndex(2026, 2) {
		t.Fatalf("ventana de marzo 2026: %d..%d", s, e)
	}
	// Más allá de los datos se usa la última ventana disponible (oct-2025..sep-2026).
	s, e = h.Window(MonthIndex(2027, 1))
	if s != MonthIndex(2025, 10) || e != MonthIndex(2026, 9) {
		t.Fatalf("ventana de enero 2027: %d..%d", s, e)
	}
	if mix, tot := h.Recent(0, 0); tot != 0 || mix != [NumClasses]uint8{} {
		t.Fatal("enero 2018 no tiene historia previa")
	}
	id, _ := cat.Lookup("San Juan de Lurigancho")
	mix, tot := h.Recent(id, MonthIndex(2026, 1))
	sum := 0
	for _, v := range mix {
		sum += int(v)
	}
	if tot == 0 || sum < 98 || sum > 102 {
		t.Fatalf("mezcla de SJL: %v (total %d)", mix, tot)
	}
	x := Vector(FSTB2, cat, h, id, 0, 1, 2026)
	if len(x) != SchemaFor(FSTB2, cat).Len() || len(x) != 11 {
		t.Fatalf("vector tb2 de longitud %d", len(x))
	}
}
