// Package sysmetrics mide el uso de CPU y memoria del propio proceso leyendo
// /proc (Linux, es decir, dentro del contenedor). En otros sistemas devuelve
// ok=false y el panel lo muestra como "no disponible".
package sysmetrics

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// clkTck es USER_HZ: los tiempos de /proc/<pid>/stat vienen en 1/100 s en Linux.
const clkTck = 100

// Sampler calcula el % de CPU entre dos lecturas consecutivas.
type Sampler struct {
	mu       sync.Mutex
	lastCPU  float64 // segundos de CPU acumulados (utime + stime)
	lastWall time.Time
	cpuPct   float64
	rssMB    float64
	ok       bool
}

// Sample toma una lectura nueva y devuelve (CPU % de un núcleo, RSS en MB, ok).
func (s *Sampler) Sample() (float64, float64, bool) {
	cpu, okCPU := processCPUSeconds()
	rss, okRSS := residentMB()
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if okCPU && !s.lastWall.IsZero() {
		if dt := now.Sub(s.lastWall).Seconds(); dt > 0 {
			s.cpuPct = 100 * (cpu - s.lastCPU) / dt
			if s.cpuPct < 0 {
				s.cpuPct = 0
			}
		}
	}
	if okCPU {
		s.lastCPU, s.lastWall = cpu, now
	}
	if okRSS {
		s.rssMB = rss
	}
	s.ok = okCPU && okRSS
	return s.cpuPct, s.rssMB, s.ok
}

// Last devuelve la última lectura sin tomar una nueva.
func (s *Sampler) Last() (float64, float64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cpuPct, s.rssMB, s.ok
}

// Run toma una muestra cada intervalo hasta que se cierre stop.
func (s *Sampler) Run(interval time.Duration, stop <-chan struct{}) {
	s.Sample()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			s.Sample()
		}
	}
}

func processCPUSeconds() (float64, bool) {
	b, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0, false
	}
	// El campo 2 (comm) va entre paréntesis y puede tener espacios: se corta tras ")".
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(s[i+1:])
	// Tras ")" el primer campo es el 3 (state); utime y stime son los campos 14 y 15.
	if len(f) < 13 {
		return 0, false
	}
	ut, err1 := strconv.ParseFloat(f[11], 64)
	st, err2 := strconv.ParseFloat(f[12], 64)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return (ut + st) / clkTck, true
}

func residentMB() (float64, bool) {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseFloat(f[1], 64)
	if err != nil {
		return 0, false
	}
	return pages * float64(os.Getpagesize()) / (1 << 20), true
}
