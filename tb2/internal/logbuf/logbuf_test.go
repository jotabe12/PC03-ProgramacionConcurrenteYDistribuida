package logbuf

import (
	"fmt"
	"log"
	"sync"
	"testing"
)

func TestRingKeepsLastN(t *testing.T) {
	r := New(5)
	for i := 1; i <= 8; i++ {
		r.Add("coordinator", "info", fmt.Sprintf("linea %d", i))
	}
	got := r.Last(10, "", "")
	if len(got) != 5 || got[0].Text != "linea 4" || got[4].Text != "linea 8" || got[4].Seq != 8 {
		t.Fatalf("contenido inesperado: %+v", got)
	}
	r.Add("ml-node-1", "error", "falla")
	if e := r.Last(10, "error", ""); len(e) != 1 || e[0].Source != "ml-node-1" {
		t.Fatalf("filtro por nivel: %+v", e)
	}
	if e := r.Last(2, "", "coordinator"); len(e) != 2 || e[1].Text != "linea 8" {
		t.Fatalf("filtro por fuente: %+v", e)
	}
}

func TestWriterAndClassify(t *testing.T) {
	r := New(10)
	l := log.New(Writer{Ring: r, Source: "coordinator"}, "", log.LstdFlags|log.Lmicroseconds)
	l.Printf("GET /api/v1/health -> 200 (0.1 ms)")
	l.Printf("nodo ml-node-2 desconectado")
	l.Printf("reentrenamiento de ml-node-1 fallido")
	acc := r.Last(10, "acceso", "")
	if len(acc) != 1 || acc[0].Text != "GET /api/v1/health -> 200 (0.1 ms)" {
		t.Fatalf("accesos: %+v", acc)
	}
	got := r.Last(10, "", "")
	if len(got) != 2 || got[0].Level != "warn" || got[1].Level != "error" {
		t.Fatalf("%+v", got)
	}
}

func TestConcurrentAdds(t *testing.T) {
	r := New(100)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				r.Add("x", "info", "y")
			}
		}()
	}
	wg.Wait()
	if got := r.Last(1000, "", ""); len(got) != 100 || got[99].Seq != 4000 {
		t.Fatalf("len=%d último seq=%d", len(got), got[len(got)-1].Seq)
	}
}
