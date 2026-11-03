package agent

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// parseMemAndSwapInfo parses memory and swap metrics from a /proc/meminfo reader.
// It reports the server's actual unevictable memory usage (MemTotal - MemAvailable)
// and swap usage (SwapTotal - SwapFree), matching standard Linux probe behavior.
func parseMemAndSwapInfo(r io.Reader) (memTotal, memUsed, swapTotal, swapUsed uint64, ok bool) {
	var memFree, memAvailable, buffers, cached, swapFree uint64
	hasAvailable := false

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		fields := strings.Fields(parts[1])
		if len(fields) == 0 {
			continue
		}
		val, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		valBytes := val * 1024 // kB -> bytes

		switch key {
		case "MemTotal":
			memTotal = valBytes
		case "MemFree":
			memFree = valBytes
		case "MemAvailable":
			memAvailable = valBytes
			hasAvailable = true
		case "Buffers":
			buffers = valBytes
		case "Cached":
			cached = valBytes
		case "SwapTotal":
			swapTotal = valBytes
		case "SwapFree":
			swapFree = valBytes
		}
	}

	if memTotal == 0 {
		return 0, 0, 0, 0, false
	}

	if hasAvailable {
		if memTotal >= memAvailable {
			memUsed = memTotal - memAvailable
		} else {
			memUsed = 0
		}
	} else {
		if memTotal >= memFree+buffers+cached {
			memUsed = memTotal - memFree - buffers - cached
		} else {
			memUsed = 0
		}
	}

	if swapTotal >= swapFree {
		swapUsed = swapTotal - swapFree
	}

	return memTotal, memUsed, swapTotal, swapUsed, true
}

// parseMemInfo parses memory metrics from a /proc/meminfo-formatted reader.
// It preserves backward compatibility for existing callers and tests.
func parseMemInfo(r io.Reader) (total, used uint64, ok bool) {
	memTotal, memUsed, _, _, ok := parseMemAndSwapInfo(r)
	return memTotal, memUsed, ok
}
