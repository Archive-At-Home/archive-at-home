package version

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"
)

const (
	// devVersion is the default when built without ldflags injection.
	// It is high enough to pass the server's min version check.
	devVersion = "v9.9.9"

	// checkInterval is how often to re-check for updates.
	checkInterval = 24 * time.Hour

	// checkTimeout bounds a single update check request.
	checkTimeout = 10 * time.Second

	latestReleaseURL = "https://api.github.com/repos/Archive-At-Home/archive-at-home/releases/latest"
	releasesURL      = "https://github.com/Archive-At-Home/archive-at-home/releases"
)

// Version is the node version, injected at build time via ldflags.
var Version = devVersion

var (
	mu         sync.RWMutex
	newVersion string
)

// NewVersion returns the latest release version found by the update check,
// or "" if none is known yet.
func NewVersion() string {
	mu.RLock()
	defer mu.RUnlock()
	return newVersion
}

// StartUpdateCheck checks for a new release immediately (async) and then
// every checkInterval until ctx is cancelled. Dev builds are skipped.
func StartUpdateCheck(ctx context.Context) {
	if Version == devVersion {
		log.Printf("[update] skipping update check (dev build %s)", Version)
		return
	}

	go func() {
		check()
		ticker := time.NewTicker(checkInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				check()
			}
		}
	}()
}

func check() {
	client := &http.Client{Timeout: checkTimeout}
	resp, err := client.Get(latestReleaseURL)
	if err != nil {
		log.Printf("[update] check failed: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("[update] check failed: unexpected status %d", resp.StatusCode)
		return
	}

	var rel struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		log.Printf("[update] check failed: %v", err)
		return
	}
	if rel.TagName == "" || rel.TagName == Version {
		return
	}

	mu.Lock()
	newVersion = rel.TagName
	mu.Unlock()
	log.Printf("[update] new version %s available (current %s): %s", rel.TagName, Version, releasesURL)
}
