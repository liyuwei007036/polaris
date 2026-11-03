//go:build linux

package agent

import (
	"bufio"
	"bytes"
	"math"
	"os"
	"runtime"
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
	memTotal, memUsed, swapTotal, swapUsed, memOk := readMemAndSwapInfo()
	diskTotal, diskUsed, diskOk := readDiskInfo("/")
	cpuPercent, cpuOk := readCPUPercent()
	uptime := readUptime()
	loadAvg, _ := readLoadAvg()
	tcpCount := countSockets("/proc/net/tcp", "/proc/net/tcp6")
	udpCount := countSockets("/proc/net/udp", "/proc/net/udp6")
	procsCount := readProcs()
	netRxRate, netTxRate := ReadNetRates()
	cpuName, cpuCores := readCPUInfo()
	osName := readOSName()
	kernel := readKernel()
	virt := readVirt()

	if !memOk && !diskOk && !cpuOk {
		return HostResources{}, false
	}

	return HostResources{
		CPUPercent:    math.Round(cpuPercent*10) / 10,
		LoadAvg:       loadAvg,
		MemoryTotal:   memTotal,
		MemoryUsed:    memUsed,
		SwapTotal:     swapTotal,
		SwapUsed:      swapUsed,
		DiskTotal:     diskTotal,
		DiskUsed:      diskUsed,
		UptimeSeconds: uptime,
		TCP:           tcpCount,
		UDP:           udpCount,
		Procs:         procsCount,
		NetRxRate:     netRxRate,
		NetTxRate:     netTxRate,
		OSName:        osName,
		Kernel:        kernel,
		Virt:          virt,
		CPUName:       cpuName,
		CPUCores:      cpuCores,
	}, true
}

func readMemAndSwapInfo() (memTotal, memUsed, swapTotal, swapUsed uint64, ok bool) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, 0, 0, false
	}
	defer file.Close()
	return parseMemAndSwapInfo(file)
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

func readLoadAvg() ([3]float64, bool) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return [3]float64{}, false
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return [3]float64{}, false
	}
	l1, _ := strconv.ParseFloat(fields[0], 64)
	l5, _ := strconv.ParseFloat(fields[1], 64)
	l15, _ := strconv.ParseFloat(fields[2], 64)
	return [3]float64{
		math.Round(l1*100) / 100,
		math.Round(l5*100) / 100,
		math.Round(l15*100) / 100,
	}, true
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

func readProcs() uint32 {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	var count uint32
	for _, e := range entries {
		if e.IsDir() {
			n := e.Name()
			if len(n) > 0 && n[0] >= '0' && n[0] <= '9' {
				count++
			}
		}
	}
	return count
}

func countSockets(paths ...string) uint32 {
	var count uint32
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		if scanner.Scan() {
			// skip header
		}
		for scanner.Scan() {
			count++
		}
		f.Close()
	}
	return count
}

func readCPUInfo() (name string, cores uint32) {
	file, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return "", 1
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "model name") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 && name == "" {
				name = strings.TrimSpace(parts[1])
			}
		}
		if strings.HasPrefix(line, "processor") {
			cores++
		}
	}
	if cores == 0 {
		cores = 1
	}
	return name, cores
}

func readOSName() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return runtime.GOOS
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
		}
	}
	return runtime.GOOS
}

func readKernel() string {
	data, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err == nil {
		return strings.TrimSpace(string(data))
	}
	return ""
}

func readVirt() string {
	if data, err := os.ReadFile("/sys/class/dmi/id/product_name"); err == nil {
		prod := strings.ToLower(strings.TrimSpace(string(data)))
		switch {
		case strings.Contains(prod, "kvm"):
			return "kvm"
		case strings.Contains(prod, "qemu"):
			return "qemu"
		case strings.Contains(prod, "vmware"):
			return "vmware"
		case strings.Contains(prod, "virtualbox"):
			return "virtualbox"
		}
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "docker"
	}
	if _, err := os.Stat("/proc/user_beancounters"); err == nil {
		return "openvz"
	}
	return "none"
}
