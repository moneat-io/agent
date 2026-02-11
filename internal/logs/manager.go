package logs

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moneat/agent/internal/config"
	"github.com/moneat/agent/internal/transport"
)

const (
	dockerSocketPath = "/var/run/docker.sock"
	defaultAPIVer    = "v1.44"
)

type Manager struct {
	cfg        *config.Config
	client     *transport.Client
	httpClient *http.Client
	hostname   string

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	entryCh chan transport.AgentLogEntry

	mu       sync.Mutex
	watchers map[string]watcherState
}

type watcherState struct {
	cancel context.CancelFunc
	meta   containerMeta
}

type containerMeta struct {
	ID          string
	Name        string
	Image       string
	Service     string
	Environment string
	Labels      map[string]string
	Tags        map[string]string
}

type dockerContainer struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	Labels map[string]string `json:"Labels"`
}

func NewManager(cfg *config.Config, client *transport.Client) *Manager {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}

	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", dockerSocketPath)
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		cfg:        cfg,
		client:     client,
		httpClient: &http.Client{Transport: tr, Timeout: 0},
		hostname:   hostname,
		ctx:        ctx,
		cancel:     cancel,
		entryCh:    make(chan transport.AgentLogEntry, 2000),
		watchers:   make(map[string]watcherState),
	}
}

func (m *Manager) Start() {
	if !m.cfg.LogsEnabled {
		return
	}

	if _, err := os.Stat(dockerSocketPath); err != nil {
		log.Printf("Docker socket unavailable (%s): %v", dockerSocketPath, err)
		return
	}

	log.Printf("Container log collection enabled (mode=%s, batch=%d/%s)",
		m.cfg.LogMode,
		m.cfg.LogBatchSize,
		m.cfg.LogBatchInterval,
	)

	m.wg.Add(2)
	go func() {
		defer m.wg.Done()
		m.discoveryLoop()
	}()
	go func() {
		defer m.wg.Done()
		m.batchLoop()
	}()
}

func (m *Manager) Stop() {
	m.cancel()
	m.stopAllWatchers()
	m.wg.Wait()
}

func (m *Manager) discoveryLoop() {
	ticker := time.NewTicker(m.cfg.LogDiscoveryInterval)
	defer ticker.Stop()

	for {
		m.reconcileContainers()
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) reconcileContainers() {
	containers, err := m.listContainers(m.ctx)
	if err != nil {
		log.Printf("Failed to list Docker containers for logs: %v", err)
		return
	}

	target := make(map[string]containerMeta)
	for _, container := range containers {
		meta := toContainerMeta(container)
		if m.shouldCollect(meta) {
			target[meta.ID] = meta
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	for id, meta := range target {
		if _, ok := m.watchers[id]; ok {
			continue
		}

		watchCtx, cancel := context.WithCancel(m.ctx)
		m.watchers[id] = watcherState{cancel: cancel, meta: meta}

		m.wg.Add(1)
		go func(cm containerMeta, ctx context.Context) {
			defer m.wg.Done()
			m.watchContainer(ctx, cm)
		}(meta, watchCtx)

		log.Printf("Started log stream for container %s (%s)", meta.Name, shortID(meta.ID))
	}

	for id, watcher := range m.watchers {
		if _, ok := target[id]; ok {
			continue
		}
		watcher.cancel()
		delete(m.watchers, id)
		log.Printf("Stopped log stream for container %s (%s)", watcher.meta.Name, shortID(id))
	}
}

func (m *Manager) stopAllWatchers() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, watcher := range m.watchers {
		watcher.cancel()
		delete(m.watchers, id)
	}
}

func (m *Manager) listContainers(ctx context.Context) ([]dockerContainer, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://unix/%s/containers/json?all=0", dockerAPIVersion()), nil)
	if err != nil {
		return nil, err
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("docker API returned %d: %s", resp.StatusCode, string(body))
	}

	var containers []dockerContainer
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return nil, err
	}

	return containers, nil
}

func (m *Manager) shouldCollect(meta containerMeta) bool {
	mode := strings.ToLower(strings.TrimSpace(m.cfg.LogMode))
	if mode == "" {
		mode = "all"
	}

	nameKey := strings.ToLower(meta.Name)
	_, includedByName := m.cfg.LogContainers[nameKey]
	_, excludedByName := m.cfg.LogExclude[nameKey]

	// If labels explicitly disable logs, always skip.
	if labelBool(meta.Labels["moneat.logs"]) == triFalse {
		return false
	}

	switch mode {
	case "label":
		return labelBool(meta.Labels["moneat.logs"]) == triTrue
	case "include":
		if len(m.cfg.LogContainers) == 0 {
			return false
		}
		return includedByName
	case "exclude":
		return !excludedByName
	case "all":
		fallthrough
	default:
		return !excludedByName
	}
}

func (m *Manager) watchContainer(ctx context.Context, meta containerMeta) {
	since := time.Now().Add(-2 * time.Second).Unix()

	for {
		if ctx.Err() != nil {
			return
		}

		err := m.streamContainerLogs(ctx, meta, &since)
		if err == nil {
			return
		}
		if ctx.Err() != nil {
			return
		}

		log.Printf("Log stream error for %s (%s): %v", meta.Name, shortID(meta.ID), err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (m *Manager) streamContainerLogs(ctx context.Context, meta containerMeta, since *int64) error {
	url := fmt.Sprintf(
		"http://unix/%s/containers/%s/logs?follow=1&stdout=1&stderr=1&timestamps=1&tail=0&since=%d",
		dockerAPIVersion(),
		meta.ID,
		*since,
	)

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("docker logs API returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	reader := bufio.NewReader(resp.Body)
	peek, err := reader.Peek(8)
	if err == nil && looksLikeMultiplexHeader(peek) {
		return m.readMultiplexStream(ctx, reader, meta, since)
	}

	return m.readPlainStream(ctx, reader, meta, since)
}

func (m *Manager) readMultiplexStream(ctx context.Context, reader *bufio.Reader, meta containerMeta, since *int64) error {
	var stdoutRemainder string
	var stderrRemainder string

	for {
		if ctx.Err() != nil {
			return nil
		}

		header := make([]byte, 8)
		if _, err := io.ReadFull(reader, header); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}

		streamType := header[0]
		size := binary.BigEndian.Uint32(header[4:8])
		if size == 0 {
			continue
		}

		chunk := make([]byte, size)
		if _, err := io.ReadFull(reader, chunk); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return err
		}

		if streamType == 2 {
			stderrRemainder = m.processChunk(meta, "stderr", stderrRemainder, string(chunk), since)
		} else {
			stdoutRemainder = m.processChunk(meta, "stdout", stdoutRemainder, string(chunk), since)
		}
	}
}

func (m *Manager) readPlainStream(ctx context.Context, reader *bufio.Reader, meta containerMeta, since *int64) error {
	var remainder string
	buf := make([]byte, 16*1024)

	for {
		if ctx.Err() != nil {
			return nil
		}

		n, err := reader.Read(buf)
		if n > 0 {
			remainder = m.processChunk(meta, "stdout", remainder, string(buf[:n]), since)
		}

		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func (m *Manager) processChunk(meta containerMeta, stream string, remainder, chunk string, since *int64) string {
	combined := remainder + chunk
	parts := strings.Split(combined, "\n")
	if len(parts) == 0 {
		return ""
	}

	for _, line := range parts[:len(parts)-1] {
		m.processLine(meta, stream, line, since)
	}

	return parts[len(parts)-1]
}

func (m *Manager) processLine(meta containerMeta, stream, raw string, since *int64) {
	line := strings.TrimRight(raw, "\r")
	if strings.TrimSpace(line) == "" {
		return
	}

	ts := time.Now().UTC()
	message := line
	if idx := strings.IndexByte(line, ' '); idx > 0 {
		if parsed, err := time.Parse(time.RFC3339Nano, line[:idx]); err == nil {
			ts = parsed.UTC()
			message = strings.TrimSpace(line[idx+1:])
		}
	}

	if ts.Unix() > *since {
		*since = ts.Unix()
	}

	parsed := parseStructuredMessage(message)
	level := normalizeLevel(parsed.Level)
	if level == "" {
		level = inferLevel(message)
	}

	service := meta.Service
	if service == "" {
		service = meta.Name
	}

	tags := cloneMap(meta.Tags)
	for k, v := range parsed.Tags {
		tags[k] = v
	}

	entry := transport.AgentLogEntry{
		Timestamp:      ts.Format(time.RFC3339Nano),
		TimestampMS:    ts.UnixMilli(),
		Level:          level,
		Message:        truncate(parsed.Message, 8192),
		Body:           truncate(message, 32768),
		Stream:         stream,
		Service:        service,
		Environment:    meta.Environment,
		Host:           m.hostname,
		ContainerName:  meta.Name,
		ContainerID:    meta.ID,
		ContainerImage: meta.Image,
		TraceID:        parsed.TraceID,
		SpanID:         parsed.SpanID,
		Tags:           tags,
		ResourceAttrs: map[string]string{
			"container.name":  meta.Name,
			"container.image": meta.Image,
			"host.name":       m.hostname,
		},
	}

	select {
	case m.entryCh <- entry:
	default:
		log.Printf("Dropping log line: buffer full (container=%s)", meta.Name)
	}
}

func (m *Manager) batchLoop() {
	ticker := time.NewTicker(m.cfg.LogBatchInterval)
	defer ticker.Stop()

	batch := make([]transport.AgentLogEntry, 0, m.cfg.LogBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := m.flushBatch(batch); err != nil {
			log.Printf("Failed to flush log batch (%d entries): %v", len(batch), err)
		}
		batch = batch[:0]
	}

	for {
		select {
		case <-m.ctx.Done():
			flush()
			return
		case entry := <-m.entryCh:
			batch = append(batch, entry)
			if len(batch) >= m.cfg.LogBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (m *Manager) flushBatch(batch []transport.AgentLogEntry) error {
	maxRetries := 3
	backoff := 2 * time.Second

	for attempt := 1; attempt <= maxRetries; attempt++ {
		resp, err := m.client.SendLogs(batch, m.cfg.LogProjectID)
		if err == nil {
			if resp != nil && resp.Accepted > 0 {
				log.Printf("Logs sent successfully (%d accepted)", resp.Accepted)
			}
			return nil
		}

		if attempt == maxRetries {
			return err
		}

		select {
		case <-m.ctx.Done():
			return m.ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}

	return nil
}

func dockerAPIVersion() string {
	apiVersion := strings.TrimSpace(os.Getenv("DOCKER_API_VERSION"))
	if apiVersion == "" {
		return defaultAPIVer
	}

	apiVersion = strings.TrimPrefix(apiVersion, "/")
	if !strings.HasPrefix(apiVersion, "v") {
		apiVersion = "v" + apiVersion
	}

	return apiVersion
}

type triBool int

const (
	triUnknown triBool = iota
	triTrue
	triFalse
)

func labelBool(raw string) triBool {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "true", "1", "yes", "on":
		return triTrue
	case "false", "0", "no", "off":
		return triFalse
	default:
		return triUnknown
	}
}

func toContainerMeta(container dockerContainer) containerMeta {
	labels := container.Labels
	if labels == nil {
		labels = map[string]string{}
	}

	name := container.ID
	if len(container.Names) > 0 {
		name = strings.TrimPrefix(container.Names[0], "/")
	}

	service := strings.TrimSpace(labels["moneat.logs.service"])
	if service == "" {
		service = name
	}

	environment := strings.TrimSpace(labels["moneat.logs.environment"])

	tags := parseTagsLabel(labels["moneat.logs.tags"])
	return containerMeta{
		ID:          container.ID,
		Name:        name,
		Image:       container.Image,
		Service:     service,
		Environment: environment,
		Labels:      labels,
		Tags:        tags,
	}
}

func looksLikeMultiplexHeader(header []byte) bool {
	if len(header) < 8 {
		return false
	}
	streamType := header[0]
	if streamType != 1 && streamType != 2 && streamType != 0 {
		return false
	}
	return header[1] == 0 && header[2] == 0 && header[3] == 0
}

func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

type parsedMessage struct {
	Message string
	Level   string
	TraceID string
	SpanID  string
	Tags    map[string]string
}

func parseStructuredMessage(message string) parsedMessage {
	trimmed := strings.TrimSpace(message)
	result := parsedMessage{
		Message: message,
		Tags:    map[string]string{},
	}

	if !strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}") {
		return result
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return result
	}

	if val := stringField(payload, "message", "msg", "log"); val != "" {
		result.Message = val
	}
	result.Level = stringField(payload, "level", "severity", "lvl")
	result.TraceID = stringField(payload, "trace_id", "traceId")
	result.SpanID = stringField(payload, "span_id", "spanId")

	reserved := map[string]struct{}{
		"message": {}, "msg": {}, "log": {},
		"level": {}, "severity": {}, "lvl": {},
		"trace_id": {}, "traceId": {},
		"span_id": {}, "spanId": {},
		"time": {}, "timestamp": {},
	}

	for key, value := range payload {
		if _, skip := reserved[key]; skip {
			continue
		}
		s := valueToString(value)
		if s == "" {
			continue
		}
		result.Tags[key] = truncate(s, 512)
	}

	return result
}

func stringField(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if val, ok := payload[key]; ok {
			s := strings.TrimSpace(valueToString(val))
			if s != "" {
				return s
			}
		}
	}
	return ""
}

func valueToString(val any) string {
	switch t := val.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		if t {
			return "true"
		}
		return "false"
	case json.Number:
		return t.String()
	default:
		return ""
	}
}

func parseTagsLabel(raw string) map[string]string {
	result := map[string]string{}
	for _, token := range strings.Split(raw, ",") {
		pair := strings.TrimSpace(token)
		if pair == "" {
			continue
		}
		idx := strings.Index(pair, "=")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(pair[:idx])
		value := strings.TrimSpace(pair[idx+1:])
		if key == "" {
			continue
		}
		result[key] = truncate(value, 512)
	}
	return result
}

func inferLevel(message string) string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "fatal"), strings.Contains(lower, "panic"), strings.Contains(lower, "critical"):
		return "fatal"
	case strings.Contains(lower, "error"):
		return "error"
	case strings.Contains(lower, "warn"):
		return "warn"
	case strings.Contains(lower, "debug"):
		return "debug"
	case strings.Contains(lower, "trace"):
		return "trace"
	default:
		return "info"
	}
}

func normalizeLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "trace":
		return "trace"
	case "debug":
		return "debug"
	case "warn", "warning":
		return "warn"
	case "error":
		return "error"
	case "fatal", "critical", "panic":
		return "fatal"
	case "info":
		return "info"
	default:
		return ""
	}
}

func cloneMap(input map[string]string) map[string]string {
	out := make(map[string]string, len(input))
	for k, v := range input {
		out[k] = v
	}
	return out
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max]
}
