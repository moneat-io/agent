package collectors

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var AgentVersion = "dev"

type LinuxCollector struct {
	hostname     string
	prevCPUTotal uint64
	prevCPUIdle  uint64
	prevCPUCores []cpuCoreStat
	prevNetRecv  uint64
	prevNetSent  uint64
	prevNetTime  time.Time
	prevDiskRead uint64
	prevDiskWrite uint64
	prevDiskTime time.Time
}

type cpuCoreStat struct {
	total uint64
	idle  uint64
}

func NewCollector() (Collector, error) {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	
	if runtime.GOOS == "linux" {
		return &LinuxCollector{
			hostname:    hostname,
			prevNetTime: time.Now(),
			prevDiskTime: time.Now(),
		}, nil
	}
	
	return nil, fmt.Errorf("unsupported platform: %s", runtime.GOOS)
}

func (c *LinuxCollector) Collect() (*SystemMetrics, error) {
	metrics := &SystemMetrics{
		Timestamp: time.Now().Unix(),
		Host:      c.hostname,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		Version:   AgentVersion,
	}
	
	metrics.CPUPercent, metrics.CPUPerCore = c.collectCPU()
	c.collectMemory(metrics)
	c.collectDisk(metrics)
	c.collectNetwork(metrics)
	c.collectLoad(metrics)
	c.collectTemperature(metrics)
	c.collectGPU(metrics)
	c.collectBattery(metrics)
	
	if containers, err := collectDockerMetrics(); err == nil {
		metrics.Containers = containers
	} else {
		// Only log permission errors or connection refused to avoid spamming on non-docker systems
		if strings.Contains(err.Error(), "permission denied") || strings.Contains(err.Error(), "connect: connection refused") {
			fmt.Printf("Warning: Failed to collect Docker metrics: %v\n", err)
		}
	}
	
	return metrics, nil
}

func (c *LinuxCollector) collectCPU() (float64, []float64) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, nil
	}
	
	scanner := bufio.NewScanner(bytes.NewReader(data))
	var cpuPercent float64
	var corePercents []float64
	coreIdx := 0
	
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "cpu") {
			break
		}
		
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		
		var total, idle uint64
		for i := 1; i < len(fields); i++ {
			val, _ := strconv.ParseUint(fields[i], 10, 64)
			total += val
			if i == 4 {
				idle = val
			}
		}
		
		if fields[0] == "cpu" {
			if c.prevCPUTotal > 0 {
				totalDelta := float64(total - c.prevCPUTotal)
				idleDelta := float64(idle - c.prevCPUIdle)
				if totalDelta > 0 {
					cpuPercent = 100.0 * (totalDelta - idleDelta) / totalDelta
				}
			}
			c.prevCPUTotal = total
			c.prevCPUIdle = idle
		} else {
			if coreIdx < len(c.prevCPUCores) {
				prev := c.prevCPUCores[coreIdx]
				totalDelta := float64(total - prev.total)
				idleDelta := float64(idle - prev.idle)
				if totalDelta > 0 {
					corePercent := 100.0 * (totalDelta - idleDelta) / totalDelta
					corePercents = append(corePercents, corePercent)
				} else {
					corePercents = append(corePercents, 0)
				}
				c.prevCPUCores[coreIdx] = cpuCoreStat{total, idle}
			} else {
				c.prevCPUCores = append(c.prevCPUCores, cpuCoreStat{total, idle})
				corePercents = append(corePercents, 0)
			}
			coreIdx++
		}
	}
	
	return cpuPercent, corePercents
}

func (c *LinuxCollector) collectMemory(m *SystemMetrics) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return
	}
	
	memInfo := make(map[string]uint64)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		val, _ := strconv.ParseUint(fields[1], 10, 64)
		memInfo[key] = val * 1024
	}
	
	m.MemTotal = memInfo["MemTotal"]
	m.MemAvailable = memInfo["MemAvailable"]
	m.MemUsed = m.MemTotal - m.MemAvailable
	m.SwapTotal = memInfo["SwapTotal"]
	m.SwapUsed = m.SwapTotal - memInfo["SwapFree"]
}

func (c *LinuxCollector) collectDisk(m *SystemMetrics) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err == nil {
		m.DiskTotal = stat.Blocks * uint64(stat.Bsize)
		m.DiskUsed = (stat.Blocks - stat.Bfree) * uint64(stat.Bsize)
	}
	
	data, err := os.ReadFile("/proc/diskstats")
	if err != nil {
		return
	}
	
	var totalRead, totalWrite uint64
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 14 {
			continue
		}
		
		deviceName := fields[2]
		if strings.Contains(deviceName, "loop") || 
		   (len(deviceName) > 3 && deviceName[len(deviceName)-1] >= '1' && deviceName[len(deviceName)-1] <= '9') {
			continue
		}
		
		readSectors, _ := strconv.ParseUint(fields[5], 10, 64)
		writeSectors, _ := strconv.ParseUint(fields[9], 10, 64)
		totalRead += readSectors * 512
		totalWrite += writeSectors * 512
	}
	
	now := time.Now()
	duration := now.Sub(c.prevDiskTime).Seconds()
	if duration > 0 && c.prevDiskRead > 0 {
		m.DiskReadBytes = uint64(float64(totalRead-c.prevDiskRead) / duration)
		m.DiskWriteBytes = uint64(float64(totalWrite-c.prevDiskWrite) / duration)
	}
	
	c.prevDiskRead = totalRead
	c.prevDiskWrite = totalWrite
	c.prevDiskTime = now
}

func (c *LinuxCollector) collectNetwork(m *SystemMetrics) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return
	}
	
	var totalRecv, totalSent uint64
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.Contains(line, ":") {
			continue
		}
		
		parts := strings.Split(line, ":")
		if len(parts) != 2 {
			continue
		}
		
		iface := strings.TrimSpace(parts[0])
		if iface == "lo" || strings.HasPrefix(iface, "veth") || strings.HasPrefix(iface, "docker") {
			continue
		}
		
		fields := strings.Fields(parts[1])
		if len(fields) < 9 {
			continue
		}
		
		recvBytes, _ := strconv.ParseUint(fields[0], 10, 64)
		sentBytes, _ := strconv.ParseUint(fields[8], 10, 64)
		totalRecv += recvBytes
		totalSent += sentBytes
	}
	
	now := time.Now()
	duration := now.Sub(c.prevNetTime).Seconds()
	if duration > 0 && c.prevNetRecv > 0 {
		m.NetRecvBytes = uint64(float64(totalRecv-c.prevNetRecv) / duration)
		m.NetSentBytes = uint64(float64(totalSent-c.prevNetSent) / duration)
	}
	
	c.prevNetRecv = totalRecv
	c.prevNetSent = totalSent
	c.prevNetTime = now
}

func (c *LinuxCollector) collectLoad(m *SystemMetrics) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return
	}
	
	fields := strings.Fields(string(data))
	if len(fields) >= 3 {
		m.Load1, _ = strconv.ParseFloat(fields[0], 64)
		m.Load5, _ = strconv.ParseFloat(fields[1], 64)
		m.Load15, _ = strconv.ParseFloat(fields[2], 64)
	}
}

func (c *LinuxCollector) collectTemperature(m *SystemMetrics) {
	var maxTemp float64
	
	zones, _ := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
	for _, zone := range zones {
		data, err := os.ReadFile(zone)
		if err != nil {
			continue
		}
		
		temp, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
		if err != nil {
			continue
		}
		
		temp = temp / 1000.0
		if temp > maxTemp {
			maxTemp = temp
		}
	}
	
	if maxTemp > 0 {
		m.TempMax = &maxTemp
	}
}

func (c *LinuxCollector) collectGPU(m *SystemMetrics) {
	cmd := exec.Command("nvidia-smi", "--query-gpu=utilization.gpu,utilization.memory,power.draw", "--format=csv,noheader,nounits")
	output, err := cmd.Output()
	if err == nil {
		fields := strings.Split(strings.TrimSpace(string(output)), ",")
		if len(fields) >= 3 {
			gpuPercent, _ := strconv.ParseFloat(strings.TrimSpace(fields[0]), 64)
			gpuMemPercent, _ := strconv.ParseFloat(strings.TrimSpace(fields[1]), 64)
			gpuPower, _ := strconv.ParseFloat(strings.TrimSpace(fields[2]), 64)
			m.GPUPercent = &gpuPercent
			m.GPUMemPercent = &gpuMemPercent
			m.GPUPower = &gpuPower
			return
		}
	}
	
	// TODO: Add AMD (rocm-smi) and Intel (intel_gpu_top) support
}

func (c *LinuxCollector) collectBattery(m *SystemMetrics) {
	batteries, _ := filepath.Glob("/sys/class/power_supply/BAT*/capacity")
	if len(batteries) == 0 {
		return
	}
	
	data, err := os.ReadFile(batteries[0])
	if err != nil {
		return
	}
	
	capacity, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
	if err == nil {
		m.BatteryPercent = &capacity
	}
}
