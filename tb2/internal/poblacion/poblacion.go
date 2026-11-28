// Package poblacion lee la proyección de población distrital del INEI (2018-2026)
// directamente del XLSX oficial. Un .xlsx es un ZIP con XML (Office Open XML),
// así que basta con archive/zip y encoding/xml de la biblioteca estándar.
package poblacion

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Table guarda la población por UBIGEO de 6 dígitos y año.
type Table struct {
	Years    []int              // p. ej. 2018..2026
	ByUbigeo map[string][]int64 // UBIGEO -> población por año (mismo orden que Years); -1 = sin dato
	Partial  int                // distritos sin dato en algún año (creados después de 2018)
	Names    map[string]string  // UBIGEO -> nombre del distrito según el INEI
}

// Get devuelve la población del distrito en el año pedido. Fuera del rango
// publicado se usa el año más cercano (2018 hacia atrás, 2026 hacia adelante).
func (t *Table) Get(ubigeo string, year int) (int64, bool) {
	if t == nil {
		return 0, false
	}
	v, ok := t.ByUbigeo[normUbigeo(ubigeo)]
	if !ok || len(v) == 0 {
		return 0, false
	}
	i := year - t.Years[0]
	if i < 0 {
		i = 0
	}
	if i >= len(v) {
		i = len(v) - 1
	}
	// Distritos creados después de 2018 no tienen valor en los años previos
	// ("Distrito creado en 2021"): se usa el año publicado más cercano.
	for d := 0; d < len(v); d++ {
		if j := i + d; j < len(v) && v[j] >= 0 {
			return v[j], true
		}
		if j := i - d; j >= 0 && v[j] >= 0 {
			return v[j], true
		}
	}
	return 0, false
}

func normUbigeo(u string) string {
	u = strings.TrimSpace(u)
	for len(u) < 6 {
		u = "0" + u
	}
	return u
}

// Load abre el XLSX del INEI y extrae la primera hoja.
func Load(path string) (*Table, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	files := map[string]*zip.File{}
	var sheets []string
	for _, f := range zr.File {
		files[f.Name] = f
		if strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml") {
			sheets = append(sheets, f.Name)
		}
	}
	if len(sheets) == 0 {
		return nil, errors.New("el XLSX no contiene hojas")
	}
	sort.Strings(sheets)
	var shared []string
	if f, ok := files["xl/sharedStrings.xml"]; ok {
		if shared, err = readShared(f); err != nil {
			return nil, fmt.Errorf("sharedStrings: %w", err)
		}
	}
	rows, err := readSheet(files[sheets[0]], shared)
	if err != nil {
		return nil, fmt.Errorf("hoja: %w", err)
	}
	return parseRows(rows)
}

func open(f *zip.File) (io.ReadCloser, error) { return f.Open() }

// readShared devuelve la tabla de cadenas compartidas (cada <si> puede tener
// texto simple <t> o "rich text" con varios <r><t>).
func readShared(f *zip.File) ([]string, error) {
	rc, err := open(f)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	var out []string
	var cur strings.Builder
	inSI, inT := false, false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		switch e := tok.(type) {
		case xml.StartElement:
			switch e.Name.Local {
			case "si":
				inSI = true
				cur.Reset()
			case "t":
				inT = inSI
			}
		case xml.EndElement:
			switch e.Name.Local {
			case "si":
				inSI = false
				out = append(out, cur.String())
			case "t":
				inT = false
			}
		case xml.CharData:
			if inT {
				cur.Write(e)
			}
		}
	}
}

// readSheet devuelve las celdas por fila: fila -> columna ("A", "B", ...) -> texto.
func readSheet(f *zip.File, shared []string) (map[int]map[string]string, error) {
	rc, err := open(f)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	rows := map[int]map[string]string{}
	var col, typ string
	var row int
	var val strings.Builder
	inV := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		switch e := tok.(type) {
		case xml.StartElement:
			switch e.Name.Local {
			case "c":
				col, typ = "", ""
				val.Reset()
				for _, a := range e.Attr {
					switch a.Name.Local {
					case "r":
						col, row = splitRef(a.Value)
					case "t":
						typ = a.Value
					}
				}
			case "v", "t":
				inV = true
			}
		case xml.EndElement:
			switch e.Name.Local {
			case "v", "t":
				inV = false
			case "c":
				s := val.String()
				if typ == "s" {
					if i, err := strconv.Atoi(s); err == nil && i >= 0 && i < len(shared) {
						s = shared[i]
					}
				}
				if col != "" && row > 0 {
					if rows[row] == nil {
						rows[row] = map[string]string{}
					}
					rows[row][col] = strings.TrimSpace(s)
				}
			}
		case xml.CharData:
			if inV {
				val.Write(e)
			}
		}
	}
}

// splitRef separa "C10" en ("C", 10).
func splitRef(ref string) (string, int) {
	i := 0
	for i < len(ref) && ref[i] >= 'A' && ref[i] <= 'Z' {
		i++
	}
	n, _ := strconv.Atoi(ref[i:])
	return ref[:i], n
}

var yearCols = []string{"C", "D", "E", "F", "G", "H", "I", "J", "K", "L", "M", "N"}

// parseRows ubica la fila de cabecera con los años y lee los distritos
// (UBIGEO de 6 dígitos que no termina en "00"; esas filas son totales).
func parseRows(rows map[int]map[string]string) (*Table, error) {
	keys := make([]int, 0, len(rows))
	for r := range rows {
		keys = append(keys, r)
	}
	sort.Ints(keys)
	header := -1
	var years []int
	var cols []string
	for _, r := range keys {
		if y, err := strconv.Atoi(rows[r]["C"]); err == nil && y >= 2000 && y <= 2100 {
			header = r
			for _, c := range yearCols {
				y, err := strconv.Atoi(rows[r][c])
				if err != nil {
					break
				}
				years = append(years, y)
				cols = append(cols, c)
			}
			break
		}
	}
	if header < 0 || len(years) == 0 {
		return nil, errors.New("no se encontró la fila de años (2018, 2019, ...)")
	}
	t := &Table{Years: years, ByUbigeo: map[string][]int64{}, Names: map[string]string{}}
	for _, r := range keys {
		if r <= header {
			continue
		}
		u := rows[r]["A"]
		if len(u) < 5 || len(u) > 6 || strings.Trim(u, "0123456789") != "" {
			continue
		}
		u = normUbigeo(u)
		if strings.HasSuffix(u, "00") {
			continue // total de departamento o provincia
		}
		vals := make([]int64, len(cols))
		found, missing := 0, 0
		for i, c := range cols {
			f, err := strconv.ParseFloat(rows[r][c], 64)
			if err != nil {
				vals[i] = -1 // p. ej. "Distrito creado en 2021"
				missing++
				continue
			}
			vals[i] = int64(math.Round(f))
			found++
		}
		if found > 0 {
			t.ByUbigeo[u] = vals
			t.Names[u] = rows[r]["B"]
			if missing > 0 {
				t.Partial++
			}
		}
	}
	if len(t.ByUbigeo) == 0 {
		return nil, errors.New("el XLSX no contiene distritos")
	}
	return t, nil
}
