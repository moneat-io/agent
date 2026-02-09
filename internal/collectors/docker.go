package collectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultDockerHost   = "unix:///var/run/docker.sock"
	defaultDockerAPIVer = "v1.41"
)

type dockerContainerSummary struct {
	ID     string   `json:"Id"`
	Names  []string `json:"Names"`
	Image  string   `json:"Image"`
	State  string   `json:"State"`
	Status string   `json:"Status"`
}

type dockerStatsResponse struct {
	CPUStats    dockerCPUStats                `json:"cpu_stats"`
	PreCPUStats dockerCPUStats                `json:"precpu_stats"`
	MemoryStats dockerMemoryStats             `json:"memory_stats"`
	Networks    map[string]dockerNetworkStats `json:"networks"`
}

type dockerCPUStats struct {
	CPUUsage struct {
		TotalUsage  uint64   `json:"total_usage"`
		PercpuUsage []uint64 `json:"percpu_usage"`
	} `json:"cpu_usage"`
	SystemCPUUsage uint64 `json:"system_cpu_usage"`
	OnlineCPUs     uint64 `json:"online_cpus"`
}

type dockerMemoryStats struct {
	Usage uint64            `json:"usage"`
	Limit uint64            `json:"limit"`
	Stats map[string]uint64 `json:"stats"`
}

type dockerNetworkStats struct {
	RxBytes uint64 `json:"rx_bytes"`
	TxBytes uint64 `json:"tx_bytes"`
}

func collectDockerMetrics() ([]ContainerMetrics, error) {
	client, baseURL, err := newDockerClient()
	if err != nil {
		return nil, err
	}

	containers, err := listContainers(client, baseURL)
	if err != nil {
		return nil, err
	}

	metrics := make([]ContainerMetrics, 0, len(containers))
	for _, container := range containers {
		stats, err := getContainerStats(client, baseURL, container.ID)
		if err != nil {
			continue
		}

		memUsed, memLimit := effectiveMemoryUsage(stats.MemoryStats)
		netRecv, netSent := aggregateNetwork(stats.Networks)

		metrics = append(metrics, ContainerMetrics{
			Name:         normalizeContainerName(container.Names, container.ID),
			ID:           container.ID,
			Image:        container.Image,
			Status:       normalizeContainerStatus(container),
			CPUPercent:   calculateDockerCPUPercent(stats.CPUStats, stats.PreCPUStats),
			MemUsed:      memUsed,
			MemLimit:     memLimit,
			NetRecvBytes: netRecv,
			NetSentBytes: netSent,
		})
	}

	return metrics, nil
}

func newDockerClient() (*http.Client, string, error) {
	host := strings.TrimSpace(os.Getenv("DOCKER_HOST"))
	if host == "" {
		host = defaultDockerHost
	}

	if strings.HasPrefix(host, "unix://") || strings.HasPrefix(host, "/") {
		socketPath := strings.TrimPrefix(host, "unix://")
		if socketPath == "" {
			return nil, "", fmt.Errorf("invalid DOCKER_HOST: %q", host)
		}

		transport := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socketPath)
			},
		}

		return &http.Client{Transport: transport, Timeout: 5 * time.Second}, "http://docker", nil
	}

	if strings.HasPrefix(host, "tcp://") {
		host = "http://" + strings.TrimPrefix(host, "tcp://")
	}

	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		return nil, "", fmt.Errorf("unsupported DOCKER_HOST scheme: %q", host)
	}

	return &http.Client{Timeout: 5 * time.Second}, strings.TrimRight(host, "/"), nil
}

func listContainers(client *http.Client, baseURL string) ([]dockerContainerSummary, error) {
	reqURL := fmt.Sprintf("%s/%s/containers/json?all=0", strings.TrimRight(baseURL, "/"), defaultDockerAPIVer)
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker API returned status %d for container list", resp.StatusCode)
	}

	var containers []dockerContainerSummary
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return nil, err
	}

	return containers, nil
}

func getContainerStats(client *http.Client, baseURL, containerID string) (*dockerStatsResponse, error) {
	reqURL := fmt.Sprintf(
		"%s/%s/containers/%s/stats?stream=false",
		strings.TrimRight(baseURL, "/"),
		defaultDockerAPIVer,
		pathEscapeSegment(containerID),
	)
	req, err := http.NewRequest(http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("docker API returned status %d for container stats", resp.StatusCode)
	}

	var stats dockerStatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, err
	}

	return &stats, nil
}

func calculateDockerCPUPercent(curr, prev dockerCPUStats) float64 {
	if curr.CPUUsage.TotalUsage < prev.CPUUsage.TotalUsage || curr.SystemCPUUsage < prev.SystemCPUUsage {
		return 0
	}

	cpuDelta := float64(curr.CPUUsage.TotalUsage - prev.CPUUsage.TotalUsage)
	systemDelta := float64(curr.SystemCPUUsage - prev.SystemCPUUsage)
	if cpuDelta <= 0 || systemDelta <= 0 {
		return 0
	}

	onlineCPUs := curr.OnlineCPUs
	if onlineCPUs == 0 && len(curr.CPUUsage.PercpuUsage) > 0 {
		onlineCPUs = uint64(len(curr.CPUUsage.PercpuUsage))
	}
	if onlineCPUs == 0 {
		onlineCPUs = 1
	}

	return (cpuDelta / systemDelta) * float64(onlineCPUs) * 100.0
}

func effectiveMemoryUsage(mem dockerMemoryStats) (used uint64, limit uint64) {
	used = mem.Usage
	limit = mem.Limit

	if used == 0 {
		return used, limit
	}

	// Docker reports cache/inactive pages inside `usage`; subtract to align with "working set".
	for _, key := range []string{"inactive_file", "total_inactive_file", "cache"} {
		if cached, ok := mem.Stats[key]; ok && used > cached {
			used -= cached
			break
		}
	}

	return used, limit
}

func aggregateNetwork(networks map[string]dockerNetworkStats) (recv uint64, sent uint64) {
	for _, stats := range networks {
		recv += stats.RxBytes
		sent += stats.TxBytes
	}
	return recv, sent
}

func normalizeContainerName(names []string, fallbackID string) string {
	if len(names) == 0 {
		return fallbackID
	}
	name := strings.TrimPrefix(names[0], "/")
	if name == "" {
		return fallbackID
	}
	return name
}

func normalizeContainerStatus(container dockerContainerSummary) string {
	if container.State != "" {
		return container.State
	}
	if container.Status != "" {
		return strings.Fields(container.Status)[0]
	}
	return "unknown"
}

func pathEscapeSegment(value string) string {
	escaped := url.PathEscape(value)
	// Keep compatibility with Docker API paths: avoid escaping path separators in IDs.
	return strings.ReplaceAll(escaped, "%2F", "/")
}
