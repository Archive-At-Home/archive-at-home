package ehentai

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Archive-At-Home/archive-at-home/node/internal/database"
)

const (
	BaseURL   = "https://e-hentai.org"
	ExBaseURL = "https://exhentai.org"

	// Credit to GP conversion rate
	CreditsToGPRatio = 3.4

	// HTTP timeout
	HTTPTimeout = 30 * time.Second
)

// Client handles EHentai API calls
type Client struct {
	baseURL    string
	cookie     string
	httpClient *http.Client
	db         *database.DB

	// Node status
	mu            sync.RWMutex
	haveFreeQuota bool
	gpBalance     int
	todayGPCost   int
	maxGPCost     int

	// Test gallery for status checking
	testGID   string
	testToken string
}

// NewClient creates a new EHentai client
func NewClient(cookie string, useExhentai bool, maxGPCost int, dbPath string) (*Client, error) {
	baseURL := BaseURL
	if useExhentai {
		baseURL = ExBaseURL
	}

	// Initialize database
	db, err := database.NewDB(dbPath)
	if err != nil {
		return nil, fmt.Errorf("init database failed: %w", err)
	}

	c := &Client{
		baseURL:    baseURL,
		cookie:     cookie,
		maxGPCost:  maxGPCost,
		httpClient: &http.Client{Timeout: HTTPTimeout},
		db:         db,
	}

	// Load today's GP cost from database so daily limit survives restarts
	if stats, err := db.GetAggregateStats(); err != nil {
		log.Printf("[ehentai] failed to load historical stats: %v", err)
	} else {
		c.todayGPCost = stats.TodayGP
	}

	// Fetch a test gallery ID for status checking
	if err := c.initTestGallery(); err != nil {
		db.Close()
		return nil, fmt.Errorf("init test gallery failed: %w", err)
	}

	return c, nil
}

// doRequest performs an HTTP request with cookie authentication
func (c *Client) doRequest(method, url string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", c.cookie)
	if method == "POST" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	return c.httpClient.Do(req)
}

func (c *Client) initTestGallery() error {
	resp, err := c.doRequest("GET", c.baseURL, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	// Extract a gallery ID from the homepage
	re := regexp.MustCompile(regexp.QuoteMeta(c.baseURL) + `/g/(\d+)/([0-9a-f]{10})`)
	matches := re.FindStringSubmatch(string(body))
	if len(matches) < 3 {
		return fmt.Errorf("no gallery found on homepage")
	}

	c.testGID = matches[1]
	c.testToken = matches[2]

	return nil
}

// RefreshStatus updates the node's free quota and GP balance
func (c *Client) RefreshStatus() error {
	archiveURL := fmt.Sprintf("%s/archiver.php?gid=%s&token=%s", c.baseURL, c.testGID, c.testToken)

	resp, err := c.doRequest("GET", archiveURL, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	html := string(body)

	// Check if free quota is available
	haveFree := strings.Contains(html, "<strong>Free!</strong>")

	// Extract GP and Credits
	gpRe := regexp.MustCompile(`([\d,]+)\s+GP.*?([\d,]+)\s+Credits`)
	matches := gpRe.FindStringSubmatch(html)

	var totalGP int
	if len(matches) >= 3 {
		gpStr := strings.ReplaceAll(matches[1], ",", "")
		creditsStr := strings.ReplaceAll(matches[2], ",", "")

		gp, err := strconv.Atoi(gpStr)
		if err != nil {
			return fmt.Errorf("parse GP failed: %w", err)
		}
		credits, err := strconv.Atoi(creditsStr)
		if err != nil {
			return fmt.Errorf("parse credits failed: %w", err)
		}

		totalGP = gp + int(float64(credits)*CreditsToGPRatio)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.haveFreeQuota = haveFree
	if len(matches) >= 3 {
		c.gpBalance = c.calculateAvailableBalance(totalGP)
	} else {
		c.gpBalance = 0
	}

	return nil
}

// GetStatus returns the current node status
func (c *Client) GetStatus() (haveFreeQuota bool, gpBalance int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.haveFreeQuota, c.gpBalance
}

// GetTodayGPCost returns today's GP cost
func (c *Client) GetTodayGPCost() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.todayGPCost
}

// calculateAvailableBalance calculates available GP balance considering daily limit
func (c *Client) calculateAvailableBalance(totalGP int) int {
	// No daily limit
	if c.maxGPCost == -1 {
		return totalGP
	}

	// Calculate remaining daily budget
	remaining := c.maxGPCost - c.todayGPCost
	if remaining < 0 {
		remaining = 0
	}

	// Return the minimum of total GP and remaining budget
	if totalGP < remaining {
		return totalGP
	}
	return remaining
}

// GetHistoricalStats returns aggregate statistics from the database
func (c *Client) GetHistoricalStats() (*database.AggregateStats, error) {
	return c.db.GetAggregateStats()
}

// ResetDailyGPCost resets the daily GP cost counter (should be called every 24 hours)
func (c *Client) ResetDailyGPCost() {
	c.mu.Lock()
	c.todayGPCost = 0
	c.mu.Unlock()
}

// GetArchiveURL requests E-Hentai to generate an archive and returns the download URL, actual GP cost, and estimated size.
// Note: This function only obtains the download link; it does NOT download the actual archive file.
func (c *Client) GetArchiveURL(gid, token string) (archiveURL string, actualGP int, sizeMiB float64, err error) {
	archiverURL := fmt.Sprintf("%s/archiver.php?gid=%s&token=%s", c.baseURL, gid, token)

	// First, check the cost
	resp, err := c.doRequest("GET", archiverURL, nil)
	if err != nil {
		return "", 0, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, 0, err
	}

	html := string(body)

	// Extract cost
	costRe := regexp.MustCompile(`<strong>(.*?)</strong>`)
	costMatches := costRe.FindStringSubmatch(html)
	if len(costMatches) < 2 {
		return "", 0, 0, fmt.Errorf("cannot find cost info")
	}

	costText := costMatches[1]
	if costText == "Free!" {
		actualGP = 0
	} else {
		// Extract numeric cost and support comma-separated values (e.g., "1,234 GP")
		numberRe := regexp.MustCompile(`[\d,]+`)
		numberMatch := numberRe.FindString(costText)
		if numberMatch != "" {
			actualGP, err = strconv.Atoi(strings.ReplaceAll(numberMatch, ",", ""))
			if err != nil {
				return "", 0, 0, fmt.Errorf("parse cost failed: %w", err)
			}
		}
	}

	// Extract Estimated Size
	sizeRe := regexp.MustCompile(`Estimated\s*Size:.*?<strong>(.*?)</strong>`)
	sizeMatches := sizeRe.FindStringSubmatch(html)
	if len(sizeMatches) >= 2 {
		sizeStr := strings.TrimSpace(sizeMatches[1])
		if size, parseErr := database.ParseSizeToMiB(sizeStr); parseErr == nil {
			sizeMiB = size
		}
	}

	// Request archive generation
	formData := url.Values{}
	formData.Set("dltype", "org")
	formData.Set("dlcheck", "Download+Original+Archive")

	resp2, err := c.doRequest("POST", archiverURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return "", 0, sizeMiB, err
	}
	defer resp2.Body.Close()

	body2, err := io.ReadAll(resp2.Body)
	if err != nil {
		return "", 0, sizeMiB, err
	}

	// Extract archive download URL from response
	urlRe := regexp.MustCompile(`document\.location = "(.*?)";`)
	urlMatches := urlRe.FindStringSubmatch(string(body2))
	if len(urlMatches) < 2 {
		return "", 0, sizeMiB, fmt.Errorf("cannot find archive download URL")
	}

	downloadedURL := urlMatches[1]
	downloadedURL = strings.TrimSuffix(downloadedURL, "?autostart=1")
	archiveURL = downloadedURL + "?start=1"

	// GP was deducted by the POST - update today's cost
	c.mu.Lock()
	c.todayGPCost += actualGP
	c.mu.Unlock()

	// Invalidate sessions (cleanup)
	invalidateData := url.Values{}
	invalidateData.Set("invalidate_sessions", "1")
	cleanupResp, cleanupErr := c.doRequest("POST", archiverURL, strings.NewReader(invalidateData.Encode()))
	if cleanupErr == nil {
		cleanupResp.Body.Close()
	}

	// Store parse log to database
	parseLog := &database.ParseLog{
		GID:           gid,
		Token:         token,
		ActualGP:      actualGP,
		EstimatedSize: sizeMiB,
		CreatedAt:     time.Now(),
	}
	if dbErr := c.db.InsertParseLog(parseLog); dbErr != nil {
		log.Printf("[ehentai] failed to insert parse log: %v", dbErr)
	}

	return archiveURL, actualGP, sizeMiB, nil
}
