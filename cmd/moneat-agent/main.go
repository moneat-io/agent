package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/moneat/agent/internal/collectors"
	"github.com/moneat/agent/internal/config"
	"github.com/moneat/agent/internal/transport"
)

func main() {
	log.SetPrefix("[moneat-agent] ")
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	
	cfg := config.LoadFromEnv()
	
	if cfg.AgentKey == "" {
		log.Fatal("MONEAT_KEY environment variable is required")
	}
	
	log.Printf("Starting Moneat Agent v%s", collectors.AgentVersion)
	log.Printf("Server: %s", cfg.MoneatURL)
	log.Printf("Poll interval: %v", cfg.PollInterval)
	
	collector, err := collectors.NewCollector()
	if err != nil {
		log.Fatalf("Failed to create collector: %v", err)
	}
	
	client := transport.NewClient(cfg)
	
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()
	
	collectAndSend(collector, client, cfg, &ticker)
	
	for {
		select {
		case <-ticker.C:
			collectAndSend(collector, client, cfg, &ticker)
			
		case sig := <-sigChan:
			log.Printf("Received signal %v, shutting down...", sig)
			return
		}
	}
}

func collectAndSend(collector collectors.Collector, client *transport.Client, cfg *config.Config, ticker **time.Ticker) {
	metrics, err := collector.Collect()
	if err != nil {
		log.Printf("Failed to collect metrics: %v", err)
		return
	}
	
	maxRetries := 3
	backoff := 5 * time.Second
	
	for attempt := 1; attempt <= maxRetries; attempt++ {
		resp, err := client.SendMetrics(metrics)
		if err != nil {
			if attempt < maxRetries {
				log.Printf("Failed to send metrics (attempt %d/%d): %v. Retrying in %v...", 
					attempt, maxRetries, err, backoff)
				time.Sleep(backoff)
				backoff *= 2
				continue
			} else {
				log.Printf("Failed to send metrics after %d attempts: %v", maxRetries, err)
				return
			}
		}
		
		if !resp.Success {
			log.Printf("Server returned success=false: %s", resp.Message)
			return
		}
		
		if resp.IntervalSeconds > 0 && resp.IntervalSeconds != int(cfg.PollInterval.Seconds()) {
			newInterval := time.Duration(resp.IntervalSeconds) * time.Second
			cfg.PollInterval = newInterval
			log.Printf("Poll interval updated to %v", cfg.PollInterval)
			
			(*ticker).Stop()
			*ticker = time.NewTicker(newInterval)
		}
		
		log.Printf("Metrics sent successfully (CPU: %.1f%%, Mem: %.1f%%, Disk: %.1f%%)",
			metrics.CPUPercent,
			float64(metrics.MemUsed)/float64(metrics.MemTotal)*100,
			float64(metrics.DiskUsed)/float64(metrics.DiskTotal)*100,
		)
		return
	}
}
