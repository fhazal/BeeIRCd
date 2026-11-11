package main

import (
	"log"
	"os"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"
)

// startProfile writes a CPU profile when BEEIRCD_CPUPROFILE is set, as
// "file:seconds" (e.g. /tmp/bee.prof:15), starting at once. It is for
// measuring (see ENGINEERING.md); servers never need it.
func startProfile(logger *log.Logger) {
	spec := os.Getenv("BEEIRCD_CPUPROFILE")
	if spec == "" {
		return
	}
	path, secs, _ := strings.Cut(spec, ":")
	n, err := strconv.Atoi(secs)
	if err != nil || n <= 0 {
		n = 15
	}
	f, err := os.Create(path)
	if err != nil {
		logger.Printf("cpu profile: %v", err)
		return
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		logger.Printf("cpu profile: %v", err)
		f.Close()
		return
	}
	logger.Printf("cpu profile to %s for %ds", path, n)
	time.AfterFunc(time.Duration(n)*time.Second, func() {
		pprof.StopCPUProfile()
		f.Close()
		logger.Printf("cpu profile written to %s", path)
	})
}

// startMemStats logs Go memory figures every BEEIRCD_MEMSTATS seconds (for
// measuring, see ENGINEERING.md).
func startMemStats(logger *log.Logger) {
	n, err := strconv.Atoi(os.Getenv("BEEIRCD_MEMSTATS"))
	if err != nil || n <= 0 {
		return
	}
	go func() {
		var m runtime.MemStats
		for range time.Tick(time.Duration(n) * time.Second) {
			runtime.ReadMemStats(&m)
			logger.Printf("mem: heap live %d MB, heap in use %d MB, stacks %d MB, from OS %d MB, goroutines %d, gc %d",
				m.HeapAlloc>>20, m.HeapInuse>>20, m.StackInuse>>20, m.Sys>>20, runtime.NumGoroutine(), m.NumGC)
		}
	}()
}
