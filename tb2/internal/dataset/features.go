package dataset

import (
	"fmt"
	"math"
)

// FeatureSet elige qué variables ve el modelo. Se puede cambiar por entrenamiento
// (POST /api/v1/train) para comparar conjuntos con el mismo pipeline distribuido.
type FeatureSet string

const (
	// FSPC4 reproduce la PC4: distrito, zona, franja imputada, mes y año. La franja
	// se imputa a partir de la clase verdadera (Tabla 11 de la PC3), así que filtra
	// información de la etiqueta: se conserva solo para medir esa fuga (ablation).
	FSPC4 FeatureSet = "pc4"
	// FSBase usa solo variables reales del CSV: distrito, zona, mes y año.
	FSBase FeatureSet = "base"
	// FSTB2 agrega el patrón criminológico reciente del distrito: la mezcla de
	// modalidades y el volumen de los 12 meses anteriores (solo pasado, sin fuga).
	FSTB2 FeatureSet = "tb2"
)

// DefaultFeatureSet es el conjunto del modelo principal del TB2.
const DefaultFeatureSet = FSTB2

// HistWindow es la ventana del patrón reciente, en meses.
const HistWindow = 12

// VolBins es el número de niveles del volumen reciente (escala log2, 0 = sin historia).
const VolBins = 32

// Schema describe las columnas de la matriz de un conjunto de variables.
type Schema struct {
	Set         FeatureSet `json:"featureSet" bson:"featureSet"`
	Names       []string   `json:"names" bson:"names"`
	Categorical []bool     `json:"categorical" bson:"categorical"`
	Card        []int      `json:"cardinality" bson:"cardinality"`
}

func (s Schema) Len() int { return len(s.Names) }

// ParseFeatureSet valida el nombre del conjunto ("" = el del TB2).
func ParseFeatureSet(v string) (FeatureSet, error) {
	switch FeatureSet(v) {
	case "":
		return DefaultFeatureSet, nil
	case FSPC4, FSBase, FSTB2:
		return FeatureSet(v), nil
	}
	return "", fmt.Errorf("featureSet desconocido %q (use pc4, base o tb2)", v)
}

// SchemaFor arma el esquema del conjunto pedido para el catálogo dado.
func SchemaFor(fs FeatureSet, c *Catalog) Schema {
	s := Schema{Set: fs}
	add := func(name string, cat bool, card int) {
		s.Names = append(s.Names, name)
		s.Categorical = append(s.Categorical, cat)
		s.Card = append(s.Card, card)
	}
	add("Distrito (UBIGEO)", true, len(c.Districts))
	add("Zona urbana", true, NumZones)
	if fs == FSPC4 {
		add("Franja horaria (imputada)", true, NumFranjas)
	}
	add("Mes", false, 12)
	add("Año", false, MaxYearIndex+1)
	if fs == FSTB2 {
		for k := 0; k < NumClasses; k++ {
			add("% "+ClassNames[k]+" (12 meses previos)", false, 101)
		}
		add("Volumen de denuncias (12 meses previos, log2)", false, VolBins)
	}
	return s
}

// MonthIndex numera los meses desde enero de BaseYear (0).
func MonthIndex(year, month int) int { return (year-BaseYear)*12 + month - 1 }

// History guarda, por distrito y mes, las denuncias de cada modalidad. Con ella
// se calcula el patrón reciente de cualquier (distrito, mes) a partir de los
// meses ANTERIORES, igual en el coordinador y en cada nodo (mismo archivo).
type History struct {
	Last   int                   // último mes con datos (índice de MonthIndex)
	Counts [][][NumClasses]int64 // [distrito][mes] -> denuncias por modalidad
	feat   [][]histFeat          // caché: [distrito][mes objetivo]
}

type histFeat struct {
	mix [NumClasses]uint8
	vol uint8
	tot int64
}

// MaxMonths cubre las consultas admitidas (2018..2033).
const MaxMonths = (MaxYearIndex + 1) * 12

// BuildHistory agrega las filas por distrito y mes y precalcula la ventana de
// cada mes objetivo (O(distritos × meses)).
func BuildHistory(t *Table, c *Catalog) *History {
	h := &History{Last: -1, Counts: make([][][NumClasses]int64, len(c.Districts))}
	for d := range h.Counts {
		h.Counts[d] = make([][NumClasses]int64, MaxMonths)
	}
	for _, r := range t.Rows {
		id, ok := c.Index[r.District]
		if !ok {
			continue
		}
		m := MonthIndex(r.Year, r.Month)
		if m < 0 || m >= MaxMonths {
			continue
		}
		h.Counts[id][m][r.Class] += int64(r.Qty)
		if m > h.Last {
			h.Last = m
		}
	}
	h.feat = make([][]histFeat, len(c.Districts))
	for d := range h.feat {
		h.feat[d] = make([]histFeat, MaxMonths)
		for m := 0; m < MaxMonths; m++ {
			h.feat[d][m] = h.compute(d, m)
		}
	}
	return h
}

// Window devuelve el rango [desde, hasta] de meses que usa el mes objetivo m:
// los 12 meses previos; si m está más allá de los datos, la última ventana
// disponible (para pronosticar octubre de 2026 en adelante).
func (h *History) Window(m int) (int, int) {
	end := m - 1
	if end > h.Last {
		end = h.Last
	}
	start := end - HistWindow + 1
	if start < 0 {
		start = 0
	}
	return start, end
}

func (h *History) compute(d, m int) histFeat {
	var f histFeat
	start, end := h.Window(m)
	if end < 0 {
		return f
	}
	var sum [NumClasses]int64
	for k := start; k <= end; k++ {
		for c := 0; c < NumClasses; c++ {
			sum[c] += h.Counts[d][k][c]
		}
	}
	for _, v := range sum {
		f.tot += v
	}
	if f.tot == 0 {
		return f
	}
	for c := 0; c < NumClasses; c++ {
		f.mix[c] = uint8(math.Round(100 * float64(sum[c]) / float64(f.tot)))
	}
	b := int(math.Floor(2 * math.Log2(float64(f.tot)+1)))
	if b >= VolBins {
		b = VolBins - 1
	}
	f.vol = uint8(b)
	return f
}

// Recent devuelve la mezcla (%) y el total de denuncias de la ventana del mes m.
func (h *History) Recent(d, m int) ([NumClasses]uint8, int64) {
	if m < 0 {
		m = 0
	}
	if m >= MaxMonths {
		m = MaxMonths - 1
	}
	f := h.feat[d][m]
	return f.mix, f.tot
}

// MonthlyAvg es el promedio mensual de denuncias del distrito en la ventana del
// mes m (pronóstico ingenuo del volumen para el patrullaje).
func (h *History) MonthlyAvg(d, m int) float64 {
	start, end := h.Window(m)
	if end < 0 {
		return 0
	}
	var tot int64
	for k := start; k <= end; k++ {
		for c := 0; c < NumClasses; c++ {
			tot += h.Counts[d][k][c]
		}
	}
	return float64(tot) / float64(end-start+1)
}

// StreetAvg es el promedio mensual de delitos de calle (hurto, robo, extorsión y
// secuestro) del distrito en la ventana del mes m. Sirve de línea base sin modelo
// para el ranking de patrullaje ("los mismos distritos de siempre").
func (h *History) StreetAvg(d, m int) float64 {
	start, end := h.Window(m)
	if end < 0 {
		return 0
	}
	var tot int64
	for k := start; k <= end; k++ {
		for c := 0; c < NumClasses; c++ {
			if StreetCrime[c] {
				tot += h.Counts[d][k][c]
			}
		}
	}
	return float64(tot) / float64(end-start+1)
}

// Actual devuelve las denuncias reales del distrito en el mes m (si hay datos).
func (h *History) Actual(d, m int) ([NumClasses]int64, bool) {
	if m < 0 || m > h.Last {
		return [NumClasses]int64{}, false
	}
	return h.Counts[d][m], true
}

// Vector arma la fila de entrada de un (distrito, franja, mes, año) para el
// conjunto fs. franja solo se usa en FSPC4.
func Vector(fs FeatureSet, c *Catalog, h *History, district, franja, month, year int) []uint8 {
	x := make([]uint8, 0, 11)
	x = append(x, uint8(district), uint8(c.Zones[district]))
	if fs == FSPC4 {
		x = append(x, uint8(franja))
	}
	x = append(x, uint8(month-1), uint8(year-BaseYear))
	if fs == FSTB2 {
		m := MonthIndex(year, month)
		if m >= MaxMonths {
			m = MaxMonths - 1
		}
		f := h.feat[district][m]
		x = append(x, f.mix[:]...)
		x = append(x, f.vol)
	}
	return x
}
