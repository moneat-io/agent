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
  -e MONEAT_KEY="<your-agent-key>" \
  -e MONEAT_URL="https://api.moneat.dev" \
  adrianelder/moneat-agent:latest
```

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `MONEAT_KEY` | Yes | - | Agent authentication key (from dashboard) |
| `MONEAT_URL` | No | `https://api.moneat.dev` | Moneat API endpoint |
| `POLL_INTERVAL` | No | `60` | Initial poll interval in seconds (server-controlled) |

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

## Docker Socket

The agent needs read-only access to `/var/run/docker.sock` to collect container metrics. If you don't want to monitor containers, you can omit the volume mount.

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

## Supported Platforms

- Linux (amd64, arm64)
- macOS (limited metrics - network, CPU, memory)
- Windows WSL (same as Linux)

## License

MIT
