package dataset

import (
	"fmt"
	"sort"
)

// Catalog es el diccionario global de distritos. Todos los nodos lo construyen
// del mismo archivo y lo ordenan por UBIGEO, así que el id de cada distrito es
// idéntico en todo el clúster (el código anterior usaba ids locales por nodo).
type Catalog struct {
	Districts []string            `json:"districts"`
	Ubigeos   []string            `json:"ubigeos"`
	Zones     []int               `json:"zones"`
	Index     map[string]int      `json:"-"`
	Volume    []int64             `json:"volume"` // denuncias clasificables 2018–2026
	Street    []int64             `json:"street"` // denuncias de calle (hurto, robo, extorsión, secuestro)
	ByClass   [][NumClasses]int64 `json:"-"`
}

// Lookup devuelve el id del distrito (nombre normalizado o sin normalizar).
func (c *Catalog) Lookup(district string) (int, bool) {
	id, ok := c.Index[Normalize(district)]
	return id, ok
}

func BuildCatalog(t *Table) (*Catalog, error) {
	type info struct {
		ubigeo, dept string
	}
	seen := map[string]info{}
	for _, r := range t.Rows {
		if _, ok := seen[r.District]; !ok {
			seen[r.District] = info{r.Ubigeo, r.Dept}
		}
	}
	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return seen[names[i]].ubigeo < seen[names[j]].ubigeo })
	if len(names) > 64 {
		return nil, fmt.Errorf("se esperaban como máximo 64 distritos, hay %d", len(names))
	}
	c := &Catalog{Index: map[string]int{}}
	for i, n := range names {
		z := ZoneOf(seen[n].dept, n)
		if z < 0 {
			return nil, fmt.Errorf("distrito sin zona: %s", n)
		}
		c.Districts = append(c.Districts, n)
		c.Ubigeos = append(c.Ubigeos, seen[n].ubigeo)
		c.Zones = append(c.Zones, z)
		c.Index[n] = i
	}
	c.Volume = make([]int64, len(names))
	c.Street = make([]int64, len(names))
	c.ByClass = make([][NumClasses]int64, len(names))
	for _, r := range t.Rows {
		id := c.Index[r.District]
		c.Volume[id] += int64(r.Qty)
		c.ByClass[id][r.Class] += int64(r.Qty)
		if StreetCrime[r.Class] {
			c.Street[id] += int64(r.Qty)
		}
	}
	return c, nil
}

// Matrix es un conjunto desagregado en formato columnar (1 byte por variable).
type Matrix struct {
	X     [][]uint8 // X[variable][fila]
	Y     []uint8
	Count [NumClasses]int64
}

func (m *Matrix) Len() int { return len(m.Y) }

// Expand desagrega las filas (1 fila con cantidad c -> c denuncias) y conserva
// solo las denuncias del shard pedido: la denuncia global k pertenece al shard
// k mod shards. Así cada nodo recibe ~1/N de CADA celda (distrito, mes, año,
// delito): un sharding estratificado, no por distrito. La numeración k recorre
// todas las denuncias en el orden del archivo, de modo que con cualquier N la
// unión de los shards es exactamente el conjunto completo.
//
// Para FSPC4 la franja se imputa con la semilla por fila de la PC3 (el flujo se
// consume siempre, así que no depende del shard). Los demás conjuntos no usan
// franja: el vector de una fila es el mismo para todas sus denuncias.
func Expand(t *Table, c *Catalog, h *History, fs FeatureSet, shard, shards int, keep func(year int) bool) *Matrix {
	if shards < 1 {
		shards = 1
	}
	sch := SchemaFor(fs, c)
	m := &Matrix{X: make([][]uint8, sch.Len())}
	estimate := int(t.Stats.DenunciasValidas)/shards + 1
	for f := range m.X {
		m.X[f] = make([]uint8, 0, estimate)
	}
	m.Y = make([]uint8, 0, estimate)
	var k int64
	for _, r := range t.Rows {
		id := c.Index[r.District]
		use := keep == nil || keep(r.Year)
		if !use {
			k += int64(r.Qty)
			continue
		}
		// primera denuncia de esta fila que cae en el shard
		first := (int64(shard) - k%int64(shards) + int64(shards)) % int64(shards)
		if fs == FSPC4 {
			fstream := newFranjaStream(r.RowNum, r.Class)
			for j := 0; j < r.Qty; j++ {
				fr := fstream.next()
				if int64(j) >= first && (int64(j)-first)%int64(shards) == 0 {
					x := Vector(fs, c, h, id, int(fr), r.Month, r.Year)
					for f, v := range x {
						m.X[f] = append(m.X[f], v)
					}
					m.Y = append(m.Y, uint8(r.Class))
					m.Count[r.Class]++
				}
			}
		} else if first < int64(r.Qty) {
			n := (int64(r.Qty)-first-1)/int64(shards) + 1
			x := Vector(fs, c, h, id, 0, r.Month, r.Year)
			for f, v := range x {
				for j := int64(0); j < n; j++ {
					m.X[f] = append(m.X[f], v)
				}
			}
			for j := int64(0); j < n; j++ {
				m.Y = append(m.Y, uint8(r.Class))
			}
			m.Count[r.Class] += n
		}
		k += int64(r.Qty)
	}
	return m
}

// Cell agrupa las denuncias de evaluación con el mismo vector de entrada.
type Cell struct {
	District int
	Franja   int // solo FSPC4
	Month    int
	Year     int
	Counts   [NumClasses]int64
}

// AggregateYear agrupa todas las denuncias de un año por vector de entrada
// (para evaluar el bosque distribuido con cientos de consultas en vez de
// 164,221). Con FSPC4 la franja imputada forma parte del vector, como en la PC4.
func AggregateYear(t *Table, c *Catalog, fs FeatureSet, year int) []Cell {
	type key struct{ d, f, m int }
	idx := map[key]int{}
	var cells []Cell
	for _, r := range t.Rows {
		if r.Year != year {
			continue
		}
		id := c.Index[r.District]
		if fs == FSPC4 {
			fstream := newFranjaStream(r.RowNum, r.Class)
			for j := 0; j < r.Qty; j++ {
				fr := int(fstream.next())
				kk := key{id, fr, r.Month}
				i, ok := idx[kk]
				if !ok {
					i = len(cells)
					idx[kk] = i
					cells = append(cells, Cell{District: id, Franja: fr, Month: r.Month, Year: year})
				}
				cells[i].Counts[r.Class]++
			}
			continue
		}
		kk := key{id, 0, r.Month}
		i, ok := idx[kk]
		if !ok {
			i = len(cells)
			idx[kk] = i
			cells = append(cells, Cell{District: id, Month: r.Month, Year: year})
		}
		cells[i].Counts[r.Class] += int64(r.Qty)
	}
	sort.Slice(cells, func(i, j int) bool {
		a, b := cells[i], cells[j]
		if a.District != b.District {
			return a.District < b.District
		}
		if a.Month != b.Month {
			return a.Month < b.Month
		}
		return a.Franja < b.Franja
	})
	return cells
}

// ClassTotals suma denuncias por clase para los años que cumplan keep.
func ClassTotals(t *Table, keep func(year int) bool) [NumClasses]int64 {
	var out [NumClasses]int64
	for _, r := range t.Rows {
		if keep == nil || keep(r.Year) {
			out[r.Class] += int64(r.Qty)
		}
	}
	return out
}
