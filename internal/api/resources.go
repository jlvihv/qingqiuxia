package api

import (
	"os"
	"runtime"
	"strconv"
	"strings"
)

// ProcessResources reads the process's resident memory and goroutine count.
//
// Resident memory comes from the kernel rather than from the heap statistics:
// what a user cares about is what the process costs, which includes the
// allocator's free pages and the stacks, not the Go heap alone. Where
// /proc/self/statm is not there, the heap's own figure stands in.
func ProcessResources() (memoryMB uint64, goroutines int) {
	goroutines = runtime.NumGoroutine()
	if kb, ok := residentKB(); ok {
		return kb / 1024, goroutines
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.Sys / (1 << 20), goroutines
}

// residentKB returns the process's resident set size in kilobytes from
// /proc/self/statm, whose second field is the resident page count.
func residentKB() (uint64, bool) {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return pages * uint64(os.Getpagesize()) / 1024, true
}
