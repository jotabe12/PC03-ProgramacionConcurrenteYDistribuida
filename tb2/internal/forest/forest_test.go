package forest

import (
	"math/rand"
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jotabe12/cc65-tb2-grupo3/internal/dataset"
)

const csvPath = "../../data/DATASET_Denuncias Policiales - Enero 2018 a Septiembre 2026.csv"

var synthSchema = dataset.Schema{
	Names:       []string{"d", "z", "f", "m", "y"},
	Categorical: []bool{true, true, true, false, false},
	Card:        []int{10, 5, 4, 12, 16},
}

// Datos sintéticos: la clase depende del distrito (categórico) y del mes (ordinal).
func synthetic(n int) *dataset.Matrix {
	rng := rand.New(rand.NewSource(7))
	m := &dataset.Matrix{X: make([][]uint8, 5)}
	for i := 0; i < n; i++ {
		d, z, f, mo, y := uint8(rng.Intn(10)), uint8(rng.Intn(5)), uint8(rng.Intn(4)), uint8(rng.Intn(12)), uint8(rng.Intn(8))
		c := uint8(1)
		if d%3 == 0 {
			c = 0
		} else if mo >= 6 {
			c = 2
		}
		if rng.Float64() < 0.05 {
			c = uint8(rng.Intn(K))
		}
		for j, v := range []uint8{d, z, f, mo, y} {
			m.X[j] = append(m.X[j], v)
		}
		m.Y = append(m.Y, c)
		m.Count[c]++
	}
	return m
}

func row(m *dataset.Matrix, i int) []uint8 {
	x := make([]uint8, len(m.X))
	for f := range m.X {
		x[f] = m.X[f][i]
	}
	return x
}

func TestLearnsPattern(t *testing.T) {
	m := synthetic(20000)
	cfg := DefaultConfig()
	cfg.Mtry = 5
	var calls atomic.Int64
	model := Train(m, synthSchema, []int{0, 1, 2, 3}, cfg, 2, func(done, total int) {
		calls.Add(1)
		if done < 1 || done > total || total != 4 {
			t.Errorf("progreso inválido %d/%d", done, total)
		}
	})
	if calls.Load() != 4 {
		t.Fatalf("se esperaban 4 avisos de progreso, hubo %d", calls.Load())
	}
	ok := 0
	for i := 0; i < 2000; i++ {
		p := model.Predict(row(m, i))
		best := 0
		for k := range p {
			if p[k] > p[best] {
				best = k
			}
		}
		if uint8(best) == m.Y[i] {
			ok++
		}
	}
	if acc := float64(ok) / 2000; acc < 0.9 {
		t.Fatalf("exactitud %.3f en un patrón simple", acc)
	}
}

// El bosque es idéntico con 1 y con N goroutines (cada una escribe solo su árbol).
func TestDeterministicWorkers(t *testing.T) {
	m := synthetic(30000)
	ids := []int{0, 1, 2, 3, 4, 5, 6, 7}
	for _, mode := range []string{WeightNone, WeightSqrt} {
		cfg := DefaultConfig()
		cfg.ClassWeight = mode
		a := Train(m, synthSchema, ids, cfg, 1, nil)
		b := Train(m, synthSchema, ids, cfg, 8, nil)
		na, sa := a.Signature()
		nb, sb := b.Signature()
		if na != nb || sa != sb || !slices.Equal(a.Importance, b.Importance) {
			t.Fatalf("%s: bosques distintos: %d/%f vs %d/%f", mode, na, sa, nb, sb)
		}
	}
}

func TestClassWeights(t *testing.T) {
	w := ClassWeights(WeightBalanced, [K]int64{600, 300, 100, 0, 0, 0})
	if w[0] >= w[1] || w[1] >= w[2] || w[3] != 1 {
		t.Fatalf("pesos balanceados inesperados: %v", w)
	}
	if w := ClassWeights(WeightNone, [K]int64{600, 300, 100, 1, 1, 1}); w != [K]float64{1, 1, 1, 1, 1, 1} {
		t.Fatalf("sin pesos: %v", w)
	}
	if _, err := ParseClassWeight("xyz"); err == nil {
		t.Fatal("classWeight inválido aceptado")
	}
}

// Prueba con el dataset real: 32 árboles repartidos 11/11/10 en 3 shards,
// evaluados en 2026 contra la línea base de la clase mayoritaria (como la PC3),
// con el conjunto base (solo variables reales) y con el del TB2 (patrón reciente).
func TestRealDataDistributedForest(t *testing.T) {
	if _, err := os.Stat(csvPath); err != nil {
		t.Skip("CSV no disponible")
	}
	tab, err := dataset.Load(csvPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	cat, _ := dataset.BuildCatalog(tab)
	h := dataset.BuildHistory(tab, cat)
	train := func(y int) bool { return y < dataset.TestYear }
	split := [][]int{{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, {11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21}, {22, 23, 24, 25, 26, 27, 28, 29, 30, 31}}
	totals := dataset.ClassTotals(tab, train)
	base := 0
	for k := range totals {
		if totals[k] > totals[base] {
			base = k
		}
	}
	accs := map[dataset.FeatureSet]float64{}
	for _, fs := range []dataset.FeatureSet{dataset.FSBase, dataset.FSTB2} {
		sch := dataset.SchemaFor(fs, cat)
		var models []*Model
		start := time.Now()
		for s := 0; s < 3; s++ {
			m := dataset.Expand(tab, cat, h, fs, s, 3, train)
			models = append(models, Train(m, sch, split[s], DefaultConfig(), 0, nil))
		}
		cells := dataset.AggregateYear(tab, cat, fs, dataset.TestYear)
		var correct, top3, baseHits, n int64
		for _, c := range cells {
			x := dataset.Vector(fs, cat, h, c.District, c.Franja, c.Month, c.Year)
			var p [K]float64
			for _, mdl := range models {
				pp := mdl.Predict(x)
				for k := range p {
					p[k] += pp[k] * float64(len(mdl.Trees))
				}
			}
			order := []int{0, 1, 2, 3, 4, 5}
			slices.SortStableFunc(order, func(a, b int) int {
				switch {
				case p[a] > p[b]:
					return -1
				case p[a] < p[b]:
					return 1
				}
				return 0
			})
			for k, v := range c.Counts {
				n += v
				if k == order[0] {
					correct += v
				}
				if k == order[0] || k == order[1] || k == order[2] {
					top3 += v
				}
				if k == base {
					baseHits += v
				}
			}
		}
		acc, baseAcc := float64(correct)/float64(n), float64(baseHits)/float64(n)
		accs[fs] = acc
		t.Logf("%s: prueba 2026 con %d denuncias (%d vectores); exactitud %.2f%%, top-3 %.2f%%, línea base %.2f%% (%v)",
			fs, n, len(cells), 100*acc, 100*float64(top3)/float64(n), 100*baseAcc, time.Since(start).Round(time.Millisecond))
		if acc <= baseAcc {
			t.Fatalf("%s: el bosque no supera la línea base", fs)
		}
	}
	if accs[dataset.FSTB2] <= accs[dataset.FSBase] {
		t.Fatalf("el patrón reciente no mejora al conjunto base: %.4f vs %.4f", accs[dataset.FSTB2], accs[dataset.FSBase])
	}
}
