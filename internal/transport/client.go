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
	jsonData, err := json.Marshal(metrics)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal metrics: %w", err)
	}
	
	var buf bytes.Buffer
	gzipWriter := gzip.NewWriter(&buf)
	if _, err := gzipWriter.Write(jsonData); err != nil {
		return nil, fmt.Errorf("failed to compress metrics: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed to close gzip writer: %w", err)
	}
	
	url := c.config.MoneatURL + c.config.IngestPath
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
	
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, string(body))
	}
	
	var ingestResp IngestResponse
	if err := json.Unmarshal(body, &ingestResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	
	return &ingestResp, nil
}
