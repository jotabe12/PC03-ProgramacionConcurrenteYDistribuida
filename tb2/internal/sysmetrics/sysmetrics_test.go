package sysmetrics

import (
	"runtime"
	"testing"
	"time"
)

func TestSamplerMeasuresBusyLoop(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("solo Linux (/proc)")
	}
	var s Sampler
	s.Sample()
	end := time.Now().Add(300 * time.Millisecond)
	x := 0
	for time.Now().Before(end) { // ~1 núcleo ocupado
		x++
	}
	cpu, rss, ok := s.Sample()
	if !ok || rss <= 0 {
		t.Fatalf("lectura no disponible: cpu=%.1f rss=%.1f ok=%v", cpu, rss, ok)
	}
	if cpu < 40 || cpu > 400 {
		t.Fatalf("CPU de un bucle ocupado = %.1f%% (se esperaba cerca de 100%%)", cpu)
	}
	t.Logf("CPU %.1f%% de un núcleo, RSS %.1f MB (x=%d)", cpu, rss, x)
}
