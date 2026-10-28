//go:build linux

package agent

import (
	"bufio"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var (
	cpuMu       sync.Mutex
	lastCPUTime time.Time
	lastTotal   uint64
	lastIdle    uint64
)

func CollectHostResources() (HostResources, bool) {
	memTotal, memUsed, memOk := readMemInfo()
	diskTotal, diskUsed, diskOk := readDiskInfo("/")
	cpuPercent, cpuOk := readCPUPercent()
	uptime := readUptime()

	if !memOk && !diskOk && !cpuOk {
		return HostResources{}, false
	}

	return HostResources{
		CPUPercent:    math.Round(cpuPercent*10) / 10,
		MemoryTotal:   memTotal,
		MemoryUsed:    memUsed,
		DiskTotal:     diskTotal,
		DiskUsed:      diskUsed,
		UptimeSeconds: uptime,
	}, true
}

func readCPUPercent() (float64, bool) {
	file, err := os.Open("/proc/stat")
	if err != nil {
		return 0, false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		return 0, false
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, false
	}

	var total, idle uint64
	for i := 1; i < len(fields); i++ {
		val, err := strconv.ParseUint(fields[i], 10, 64)
		if err != nil {
			continue
		}
		total += val
		if i == 4 || i == 5 { // idle and iowait
			idle += val
		}
	}

	cpuMu.Lock()
	defer cpuMu.Unlock()

	now := time.Now()
	prevTotal := lastTotal
	prevIdle := lastIdle
	prevTime := lastCPUTime

	lastTotal = total
	lastIdle = idle
	lastCPUTime = now

	if prevTotal == 0 || now.Sub(prevTime) > 60*time.Second {
		load, ok := readLoadAvg1Min()
		if ok {
			return load, true
		}
		return 0, true
	}

	deltaTotal := total - prevTotal
	deltaIdle := idle - prevIdle
	if deltaTotal == 0 {
		return 0, true
	}

	percent := float64(deltaTotal-deltaIdle) / float64(deltaTotal) * 100.0
	if percent < 0 {
		percent = 0
	} else if percent > 100 {
		percent = 100
	}
	return percent, true
}

func readLoadAvg1Min() (float64, bool) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, false
	}
	load, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, false
	}
	return math.Min(100.0, load*10.0), true
}

func readMemInfo() (total, used uint64, ok bool) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, false
	}
	defer file.Close()

	var memTotal, memFree, memAvailable, buffers, cached uint64
	hasAvailable := false

	scanner := bufio.NewScanner(file)
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
		used = memTotal - memFree - buffers - cached
	}
	return memTotal, used, true
}

func readDiskInfo(path string) (total, used uint64, ok bool) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, 0, false
	}
	total = stat.Blocks * uint64(stat.Bsize)
	free := stat.Bavail * uint64(stat.Bsize)
	if total >= free {
		used = total - free
	}
	return total, used, true
}

func readUptime() uint64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	sec, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return uint64(sec)
}
