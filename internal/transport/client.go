package transport

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/moneat/agent/internal/collectors"
	"github.com/moneat/agent/internal/config"
)

type IngestResponse struct {
	Success         bool   `json:"success"`
	IntervalSeconds int    `json:"interval_seconds"`
	Message         string `json:"message,omitempty"`
}

type AgentLogEntry struct {
	Timestamp      string            `json:"timestamp,omitempty"`
	TimestampMS    int64             `json:"timestamp_ms,omitempty"`
	Level          string            `json:"level,omitempty"`
	Message        string            `json:"message"`
	Body           string            `json:"body,omitempty"`
	Stream         string            `json:"stream,omitempty"`
	Service        string            `json:"service,omitempty"`
	Environment    string            `json:"environment,omitempty"`
	Host           string            `json:"host,omitempty"`
	ContainerName  string            `json:"container_name,omitempty"`
	ContainerID    string            `json:"container_id,omitempty"`
	ContainerImage string            `json:"container_image,omitempty"`
	TraceID        string            `json:"trace_id,omitempty"`
	SpanID         string            `json:"span_id,omitempty"`
	Tags           map[string]string `json:"tags,omitempty"`
	ResourceAttrs  map[string]string `json:"resource_attributes,omitempty"`
}

type LogsRequest struct {
	ProjectID *int64          `json:"project_id,omitempty"`
	Logs      []AgentLogEntry `json:"logs"`
}

type LogsResponse struct {
	Accepted int    `json:"accepted"`
	Message  string `json:"message,omitempty"`
}

type Client struct {
	config     *config.Config
	httpClient *http.Client
}

func NewClient(cfg *config.Config) *Client {
	return &Client{
		config: cfg,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) SendMetrics(metrics *collectors.SystemMetrics) (*IngestResponse, error) {
	body, err := c.sendCompressedJSON(c.config.IngestPath, metrics, http.StatusOK)
	if err != nil {
		return nil, err
	}

	var ingestResp IngestResponse
	if err := json.Unmarshal(body, &ingestResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &ingestResp, nil
}

func (c *Client) SendLogs(entries []AgentLogEntry, projectID *int64) (*LogsResponse, error) {
	if len(entries) == 0 {
		return &LogsResponse{Accepted: 0}, nil
	}

	payload := LogsRequest{
		ProjectID: projectID,
		Logs:      entries,
	}

	body, err := c.sendCompressedJSON(c.config.LogsPath, payload, http.StatusAccepted, http.StatusOK)
	if err != nil {
		return nil, err
	}

	var logsResp LogsResponse
	if err := json.Unmarshal(body, &logsResp); err != nil {
		return nil, fmt.Errorf("failed to parse log ingestion response: %w", err)
	}

	return &logsResp, nil
}

func (c *Client) sendCompressedJSON(path string, payload any, expectedStatusCodes ...int) ([]byte, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal payload: %w", err)
	}

	var buf bytes.Buffer
	gzipWriter := gzip.NewWriter(&buf)
	if _, err := gzipWriter.Write(jsonData); err != nil {
		return nil, fmt.Errorf("failed to compress payload: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed to close gzip writer: %w", err)
	}

	url := c.config.MoneatURL + path
	req, err := http.NewRequest("POST", url, &buf)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Authorization", "Bearer "+c.config.AgentKey)
	req.Header.Set("User-Agent", "moneat-agent/"+collectors.AgentVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	for _, code := range expectedStatusCodes {
		if resp.StatusCode == code {
			return body, nil
		}
	}

	return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
}
