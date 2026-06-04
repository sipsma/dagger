package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/dagger/dagger/engine/slog"
)

const (
	cachemoneyURLEnv = "DAGGER_CACHEMONEY_URL"
	cachemoneyIDEnv  = "DAGGER_CACHEMONEY_ID"
)

func sendCachemoneyStartupProbe(ctx context.Context) {
	url := os.Getenv(cachemoneyURLEnv)
	if url == "" {
		return
	}

	client := &http.Client{Timeout: 5 * time.Second}
	for attempt := 1; attempt <= 30; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
		if err != nil {
			slog.Warn("cachemoney startup probe request invalid", "error", err)
			return
		}
		req.Header.Set("X-Dagger-Cachemoney-ID", os.Getenv(cachemoneyIDEnv))
		req.Header.Set("X-Dagger-Cachemoney-Source", "engine-startup")

		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return
			}
			slog.Warn("cachemoney startup probe failed", "status", resp.StatusCode, "attempt", attempt)
		} else {
			slog.Warn("cachemoney startup probe failed", "error", err, "attempt", attempt)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
