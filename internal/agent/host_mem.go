package agent

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// parseMemInfo parses memory metrics from a /proc/meminfo-formatted reader.
// It reports the entire server's host memory usage (MemTotal - MemFree),
// representing all memory allocated by the operating system (including
// processes, kernel structures, I/O buffers, and page caches), which matches
// what cloud provider virtualization panels (KVM / QEMU hypervisors, Proxmox,
// Virtualizor, SolusVM, and cloud vendor host consoles) display as the server's
// total memory utilization.
func parseMemInfo(r io.Reader) (total, used uint64, ok bool) {
	var memTotal, memFree uint64

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
		}
	}

	if memTotal == 0 {
		return 0, 0, false
	}

	if memTotal >= memFree {
		used = memTotal - memFree
	} else {
		used = 0
	}
	return memTotal, used, true
}
