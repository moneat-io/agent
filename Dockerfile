FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git

WORKDIR /build

COPY go.mod ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -ldflags "-w -s -X github.com/moneat/agent/internal/collectors.AgentVersion=${VERSION}" \
    -o moneat-agent ./cmd/moneat-agent

FROM alpine:3.19

RUN apk add --no-cache \
    ca-certificates \
    smartmontools \
    lm-sensors

COPY --from=builder /build/moneat-agent /usr/local/bin/moneat-agent

ENTRYPOINT ["/usr/local/bin/moneat-agent"]
