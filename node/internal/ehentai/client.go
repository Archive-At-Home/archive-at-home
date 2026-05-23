package ehentai

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
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
	homeGalleryPattern   = regexp.MustCompile(regexp.QuoteMeta(BaseURL) + `/g/(\d+)/([0-9a-f]{10})`)

	exURL, _ = url.Parse(ExBaseURL)

	ErrIgneousRevoked = fmt.Errorf("exhentai igneous cookie has become 'mystery' — access revoked")
)

// Client handles EHentai API calls
type Client struct {
	baseURL    string
	jar        *cookiejar.Jar
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

// NewClient creates a new EHentai client, auto-detecting the correct base URL.
//
// Detection flow:
//  1. HEAD e-hentai.org/home.php with ipb_member_id + ipb_pass_hash; if "star"
//     cookie is present, use e-hentai.org.
//  2. HEAD exhentai.org with the same two cookies; if "igneous" is present and
//     not "mystery", use exhentai.org.
//  3. If igneous is "mystery" or absent, check whether the cookie string already
//     contains an igneous value. If not, return an error.
//  4. HEAD exhentai.org with all three cookies; if the resulting igneous
//     becomes "mystery", return an error.
//  5. Otherwise use exhentai.org.
//
// During normal operation on exhentai, if igneous turns into "mystery"
// ErrIgneousRevoked is returned through doRequest. The caller should
// trigger a graceful shutdown when this error is received — re-acquiring igneous has
// already been tried (or the value was manually configured).
func NewClient(cookieStr string, maxGPCost int, db *database.DB) (*Client, error) {
	var memberID, passHash, configIgneous string

	configCookies, _ := http.ParseCookie(cookieStr)
	for _, c := range configCookies {
		switch c.Name {
		case "ipb_member_id":
			memberID = c.Value
		case "ipb_pass_hash":
			passHash = c.Value
		case "igneous":
			configIgneous = c.Value
		}
	}

	if memberID == "" || passHash == "" {
		return nil, fmt.Errorf("cookie must contain ipb_member_id and ipb_pass_hash")
	}

	baseURL, jar, err := detectBaseURL(memberID, passHash, configIgneous)
	if err != nil {
		return nil, err
	}

	c := &Client{
		baseURL: baseURL,
		jar:     jar,
		httpClient: &http.Client{
			Jar:     jar,
			Timeout: HTTPTimeout,
		},
		db:        db,
		maxGPCost: maxGPCost,
	}

	if err := c.initTestGallery(); err != nil {
		return nil, fmt.Errorf("init test gallery failed: %w", err)
	}

	return c, nil
}

func detectBaseURL(memberID, passHash, configIgneous string) (string, *cookiejar.Jar, error) {
	eURL, _ := url.Parse(BaseURL)
	homeURL := BaseURL + "/home.php"

	authCookies := []*http.Cookie{
		{Name: "ipb_member_id", Value: memberID},
		{Name: "ipb_pass_hash", Value: passHash},
	}

	// Step 1: try e-hentai.org — star cookie indicates donor access
	if jar := probe(eURL, homeURL, "star", authCookies); jar != nil {
		log.Print("detected e-hentai.org (star cookie found)")
		return BaseURL, jar, nil
	}

	// Step 2: try exhentai.org with only ipb_member_id + ipb_pass_hash
	if jar := probe(exURL, ExBaseURL, "igneous", authCookies); jar != nil {
		log.Print("detected exhentai.org (igneous acquired automatically)")
		// exhentai.org doesn't expose balance; RefreshStatus hits e-hentai.org
		jar.SetCookies(eURL, authCookies)
		return ExBaseURL, jar, nil
	}

	// Step 3: auto-detection failed — check for manually configured igneous
	if configIgneous == "" {
		return "", nil, fmt.Errorf("exhentai access requires an igneous cookie but none was found in the cookie config")
	}

	// Step 4: retry exhentai with the manually configured igneous
	allCookies := append(authCookies, &http.Cookie{Name: "igneous", Value: configIgneous})
	jar := probe(exURL, ExBaseURL, "igneous", allCookies)
	if jar == nil {
		return "", nil, fmt.Errorf("the igneous cookie provided in the config is invalid (exhentai returned 'mystery')")
	}
	log.Print("detected exhentai.org (using configured igneous)")
	jar.SetCookies(eURL, authCookies) // same reason as Step 2
	return ExBaseURL, jar, nil
}

// probe sends a HEAD request to headURL with the given cookies. If the
// response contains a cookie named checkName with a non-empty, non-"mystery"
// value, it returns the temp jar (which holds all cookies from the probe).
// On failure returns nil.
func probe(jarURL *url.URL, headURL, checkName string, cookies []*http.Cookie) *cookiejar.Jar {
	tempJar, _ := cookiejar.New(nil)
	tempJar.SetCookies(jarURL, cookies)

	client := &http.Client{Jar: tempJar, Timeout: HTTPTimeout}
	resp, err := client.Head(headURL)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	for _, c := range tempJar.Cookies(jarURL) {
		if c.Name == checkName && c.Value != "" && c.Value != "mystery" {
			return tempJar
		}
	}
	return nil
}

// doRequest performs an HTTP request with cookie authentication
func (c *Client) doRequest(method, url string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	if method == "POST" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	// Monitor igneous on exhentai — if it flips to "mystery" the
	// access has been revoked and cannot be recovered automatically.
	if strings.HasPrefix(url, ExBaseURL) {
		if err := c.checkIgneous(); err != nil {
			resp.Body.Close()
			return nil, err
		}
	}

	return resp, nil
}

func (c *Client) checkIgneous() error {
	for _, ck := range c.jar.Cookies(exURL) {
		if ck.Name == "igneous" && ck.Value == "mystery" {
			return ErrIgneousRevoked
		}
	}
	return nil
}

func (c *Client) initTestGallery() error {
	resp, err := c.doRequest("GET", BaseURL, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	matches := homeGalleryPattern.FindStringSubmatch(string(body))
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
	remaining := max(c.maxGPCost-recentGPCost, 0)

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
	if cleanupErr != nil {
		log.Printf("failed to invalidate sessions: %v", cleanupErr)
	} else {
		cleanupResp.Body.Close()
	}

	return archiveURL, actualGP, sizeMiB, nil
}
