package ehentai

import (
	"fmt"
	"io"
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

var (
	balancePattern       = regexp.MustCompile(`([\d,]+)\s+GP.*?([\d,]+)\s+Credits`)
	costPattern          = regexp.MustCompile(`<strong>(.*?)</strong>`)
	numberPattern        = regexp.MustCompile(`[\d,]+`)
	estimatedSizePattern = regexp.MustCompile(`Estimated\s*Size:.*?<strong>(.*?)</strong>`)
	redirectURLPattern   = regexp.MustCompile(`document\.location = "(.*?)";`)
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
	maxGPCost     int

	// Test gallery for status checking
	testGID   string
	testToken string
}

// NewClient creates a new EHentai client
func NewClient(cookie string, useExhentai bool, maxGPCost int, db *database.DB) (*Client, error) {
	baseURL := BaseURL
	if useExhentai {
		baseURL = ExBaseURL
	}

	c := &Client{
		baseURL:    baseURL,
		cookie:     cookie,
		maxGPCost:  maxGPCost,
		httpClient: &http.Client{Timeout: HTTPTimeout},
		db:         db,
	}

	// Fetch a test gallery ID for status checking
	if err := c.initTestGallery(); err != nil {
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
	archiveURL := fmt.Sprintf("%s/archiver.php?gid=%s&token=%s", BaseURL, c.testGID, c.testToken)

	resp, err := c.doRequest("GET", archiveURL, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()

		if err := c.initTestGallery(); err != nil {
			return fmt.Errorf("refresh test gallery on 404 failed: %w", err)
		}

		archiveURL = fmt.Sprintf("%s/archiver.php?gid=%s&token=%s", BaseURL, c.testGID, c.testToken)
		resp, err = c.doRequest("GET", archiveURL, nil)
		if err != nil {
			return err
		}
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
	matches := balancePattern.FindStringSubmatch(html)
	hasBalanceInfo := len(matches) >= 3
	if !hasBalanceInfo {
		c.mu.Lock()
		defer c.mu.Unlock()

		c.haveFreeQuota = haveFree
		c.gpBalance = 0
		return nil
	}

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

	totalGP := gp + int(float64(credits)*CreditsToGPRatio)

	recentGPCost, err := c.db.GetRecentGPCost()
	if err != nil {
		return fmt.Errorf("load recent gp cost failed: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.haveFreeQuota = haveFree
	c.gpBalance = c.calculateAvailableBalance(totalGP, recentGPCost)

	return nil
}

// GetStatus returns the current node status
func (c *Client) GetStatus() (haveFreeQuota bool, gpBalance int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.haveFreeQuota, c.gpBalance
}

// calculateAvailableBalance calculates available GP balance considering daily limit
func (c *Client) calculateAvailableBalance(totalGP, recentGPCost int) int {
	// No daily limit
	if c.maxGPCost == -1 {
		return totalGP
	}

	// Calculate remaining daily budget
	remaining := c.maxGPCost - recentGPCost
	if remaining < 0 {
		remaining = 0
	}

	// Return the minimum of total GP and remaining budget
	if totalGP < remaining {
		return totalGP
	}
	return remaining
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
	costMatches := costPattern.FindStringSubmatch(html)
	if len(costMatches) < 2 {
		return "", 0, 0, fmt.Errorf("cannot find cost info")
	}

	costText := costMatches[1]
	if costText == "Free!" {
		actualGP = 0
	} else {
		// Extract numeric cost and support comma-separated values (e.g., "1,234 GP")
		numberMatch := numberPattern.FindString(costText)
		if numberMatch != "" {
			actualGP, err = strconv.Atoi(strings.ReplaceAll(numberMatch, ",", ""))
			if err != nil {
				return "", 0, 0, fmt.Errorf("parse cost failed: %w", err)
			}
		}
	}

	// Extract Estimated Size
	sizeMatches := estimatedSizePattern.FindStringSubmatch(html)
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
	urlMatches := redirectURLPattern.FindStringSubmatch(string(body2))
	if len(urlMatches) < 2 {
		return "", 0, sizeMiB, fmt.Errorf("cannot find archive download URL")
	}

	downloadedURL := urlMatches[1]
	downloadedURL = strings.TrimSuffix(downloadedURL, "?autostart=1")
	archiveURL = downloadedURL + "?start=1"

	// Invalidate sessions (cleanup)
	invalidateData := url.Values{}
	invalidateData.Set("invalidate_sessions", "1")
	cleanupResp, cleanupErr := c.doRequest("POST", archiverURL, strings.NewReader(invalidateData.Encode()))
	if cleanupErr == nil {
		cleanupResp.Body.Close()
	}

	return archiveURL, actualGP, sizeMiB, nil
}
