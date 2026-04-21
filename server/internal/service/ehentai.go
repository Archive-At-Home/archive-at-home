package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/Archive-At-Home/archive-at-home/server/internal/config"
)

const (
	apiE         = "https://e-hentai.org/api.php"
	oneYear      = 365 * 24 * time.Hour
	gpPerMB      = 20
	gpMultiplier = 3
	bytesPerMB   = 1000000
)

var httpClient = &http.Client{Timeout: 15 * time.Second}

// GalleryQuota holds the quota information for a gallery
type GalleryQuota struct {
	IsNew bool // true if published within the last year
	GP    int  // GP (Gold Points) cost to download
}

func ResolveParseParams(ctx context.Context, cfg *config.Config, galleryID, galleryKey string) (*GalleryQuota, error) {
	gid, err := strconv.Atoi(galleryID)
	if err != nil {
		return nil, fmt.Errorf("invalid gallery id: %w", err)
	}

	payload, _ := json.Marshal(map[string]any{
		"method":    "gdata",
		"gidlist":   [][]any{{gid, galleryKey}},
		"namespace": 1,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiE, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("api status code: %d", resp.StatusCode)
	}

	var result struct {
		Gmetadata []struct {
			Posted   string `json:"posted"`
			Filesize int64  `json:"filesize"`
			Error    string `json:"error"`
		} `json:"gmetadata"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode json failed: %w", err)
	}

	if len(result.Gmetadata) == 0 || result.Gmetadata[0].Error != "" {
		if len(result.Gmetadata) == 0 {
			return nil, fmt.Errorf("api returned error: empty metadata")
		}
		return nil, fmt.Errorf("api returned error: %s", result.Gmetadata[0].Error)
	}

	meta := result.Gmetadata[0]
	postedUnix, err := strconv.ParseInt(meta.Posted, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid posted timestamp: %w", err)
	}

	isNew := time.Since(time.Unix(postedUnix, 0)) < oneYear
	mbSize := float64(meta.Filesize) / float64(bytesPerMB)
	gp := int(mbSize*float64(gpPerMB)) + 1
	if !isNew {
		gp *= gpMultiplier
	}

	return &GalleryQuota{
		IsNew: isNew,
		GP:    gp,
	}, nil
}
