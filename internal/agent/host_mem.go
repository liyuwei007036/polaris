package agent

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// parseMemInfo parses memory metrics from a /proc/meminfo-formatted reader.
// It reports the server's actual unevictable memory usage (MemTotal - MemAvailable),
// excluding reclaimable page caches and buffers, which accurately reflects
// real application and kernel memory footprint.
func parseMemInfo(r io.Reader) (total, used uint64, ok bool) {
	var memTotal, memFree, memAvailable, buffers, cached uint64
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
		}
	}

	if memTotal == 0 {
		return 0, 0, false
	}

	if hasAvailable {
		if memTotal >= memAvailable {
			used = memTotal - memAvailable
		} else {
			used = 0
		}
	} else {
		if memTotal >= memFree+buffers+cached {
			used = memTotal - memFree - buffers - cached
		} else {
			used = 0
		}
	}
	return memTotal, used, true
}
