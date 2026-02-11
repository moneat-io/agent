# Moneat Agent

[![Docker Pulls](https://img.shields.io/docker/pulls/adrianelder/moneat-agent)](https://hub.docker.com/r/adrianelder/moneat-agent)
[![Docker Image Size](https://img.shields.io/docker/image-size/adrianelder/moneat-agent/latest)](https://hub.docker.com/r/adrianelder/moneat-agent)
[![GitHub Release](https://img.shields.io/github/v/release/moneat/agent)](https://github.com/moneat/agent/releases)

Lightweight monitoring agent that collects system and container metrics and sends them to the Moneat platform.

## Quick Start

```bash
docker run -d --name moneat-agent \
  --restart unless-stopped \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -e DOCKER_HOST="unix:///var/run/docker.sock" \
  -e MONEAT_KEY="<your-agent-key>" \
  -e MONEAT_URL="https://api.moneat.io" \
  -e MONEAT_LOGS=true \
  adrianelder/moneat-agent:latest
```

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `MONEAT_KEY` | Yes | - | Agent authentication key (from dashboard) |
| `MONEAT_URL` | No | `https://api.moneat.io` | Moneat API endpoint |
| `DOCKER_HOST` | No | `unix:///var/run/docker.sock` | Docker Engine API endpoint used for container metrics |
| `DOCKER_API_VERSION` | No | `v1.44` | Docker Engine API version path used by the agent (e.g. `v1.44` or `1.44`) |
| `POLL_INTERVAL` | No | `60` | Initial poll interval in seconds (server-controlled) |
| `MONEAT_LOGS` | No | `false` | Enable container log collection |
| `MONEAT_LOG_MODE` | No | `all` | Log collection mode: `all`, `label`, `include`, `exclude` |
| `MONEAT_LOG_CONTAINERS` | No | - | Comma-separated container names for `include` mode |
| `MONEAT_LOG_EXCLUDE` | No | - | Comma-separated container names to exclude (`exclude`/`all` modes) |
| `MONEAT_LOG_BATCH_SIZE` | No | `100` | Max log lines per batch upload |
| `MONEAT_LOG_BATCH_INTERVAL` | No | `5` | Seconds between log batch flushes |
| `MONEAT_LOG_DISCOVERY_INTERVAL` | No | `10` | Seconds between Docker container discovery passes |
| `MONEAT_LOG_PROJECT_ID` | No | - | Optional explicit project ID for `/v1/monitor/logs` payload |

## Collected Metrics

- **CPU**: Per-core and aggregate usage percentage
- **Memory**: Total, used, available, swap usage
- **Disk**: Usage and I/O throughput
- **Network**: Bytes sent/received across all interfaces
- **Load**: 1, 5, and 15-minute load averages
- **Temperature**: Maximum temperature across all sensors
- **GPU**: Nvidia, AMD, and Intel GPU utilization (if available)
- **Battery**: Charge percentage (if available)
- **Docker**: Per-container CPU, memory, and network usage
- **Container Logs**: `stdout`/`stderr` streams with batched forwarding and label/tag enrichment

## Docker Socket

The agent needs read-only access to `/var/run/docker.sock` to collect container metrics. If you don't want to monitor containers, you can omit the volume mount.

## Container Log Collection

Set `MONEAT_LOGS=true` to enable log shipping from Docker containers to `POST /v1/monitor/logs`.

### Mode 1: Collect all containers (default)

```bash
-e MONEAT_LOGS=true
```

### Mode 2: Include/exclude by container name

```bash
# Include only specific containers
-e MONEAT_LOGS=true \
-e MONEAT_LOG_MODE=include \
-e MONEAT_LOG_CONTAINERS="web,api,worker"

# Or exclude specific containers
-e MONEAT_LOGS=true \
-e MONEAT_LOG_MODE=exclude \
-e MONEAT_LOG_EXCLUDE="redis,postgres,clickhouse"
```

If `MONEAT_LOG_MODE` is not set, `MONEAT_LOG_CONTAINERS` implies `include` mode and `MONEAT_LOG_EXCLUDE` implies `exclude` mode.

### Mode 3: Label-driven opt-in

```bash
-e MONEAT_LOGS=true \
-e MONEAT_LOG_MODE=label
```

Then annotate containers:

```yaml
services:
  web:
    labels:
      moneat.logs: "true"
      moneat.logs.service: "web-api"
      moneat.logs.environment: "production"
      moneat.logs.tags: "team=backend,component=auth"
```

## Architecture

The agent uses a **push-based** model:

1. Collects metrics from the system (reading `/proc`, calling APIs, etc.)
2. Serializes to JSON and compresses with gzip
3. POSTs to the Moneat API with agent key authentication
4. Server responds with the next poll interval (tier-based)

This design works behind NAT/firewalls without requiring any port forwarding.

## Installation

### Using Docker (Recommended)

Pull the latest image from DockerHub:

```bash
docker pull adrianelder/moneat-agent:latest
```

Available tags:
- `latest` - Latest stable release
- `1` - Latest v1.x.x release
- `1.2` - Latest v1.2.x release
- `1.2.3` - Specific version

### Building from Source

```bash
# Build binary
go build -o moneat-agent ./cmd/moneat-agent

# Build Docker image
docker build -t moneat-agent .

# Multi-arch build
docker buildx build --platform linux/amd64,linux/arm64 -t moneat-agent .
```

For maintainers creating releases, see [RELEASING.md](RELEASING.md).

## Local Development

### Run Locally

```bash
# Set required environment variables
export MONEAT_KEY="your-test-key"
export MONEAT_URL="https://api.moneat.io"  # or your local/test API endpoint

# Build and run
go build -o moneat-agent ./cmd/moneat-agent && ./moneat-agent
```

### Testing

```bash
# Run all tests
go test ./...

# Run tests with coverage
go test -cover ./...

# Run tests with verbose output
go test -v ./...
```

## Supported Platforms

- Linux (amd64, arm64)
- macOS (limited metrics - network, CPU, memory)
- Windows WSL (same as Linux)

## License

MIT
