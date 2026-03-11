package finance

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/beavermoney/finance-go/form"
)

// Printfer is an interface to be implemented by Logger.
type Printfer interface {
	Printf(format string, v ...interface{})
}

// init sets initial logger defaults.
func init() {
	Logger = log.New(os.Stderr, "", log.LstdFlags)
}

const (
	// YFinBackend is a constant representing yahoo service backend.
	YFinBackend SupportedBackend = "yahoo"
	// YFinURL is the URL of the yahoo service backend.
	YFinURL string = "https://query1.finance.yahoo.com"
	// YQuotePath is the path for quote requests.
	YQuotePath string = "/v7/finance/quote"
	// YOptionsPrefix is the path prefix for options requests.
	YOptionsPrefix string = "/v7/finance/options/"
	// BATSBackend is a constant representing the uploads service backend.
	BATSBackend SupportedBackend = "bats"
	// BATSURL is the URL of the uploads service backend.
	BATSURL string = ""

	// Private constants.
	// ------------------

	defaultHTTPTimeout = 80 * time.Second
	yFinURL            = "https://query1.finance.yahoo.com"
	yFinURL2           = "https://query2.finance.yahoo.com"
	batsURL            = ""

	// Updated URLs for authentication (similar to yfinance)
	crumbURL1  = yFinURL + "/v1/test/getcrumb"
	crumbURL2  = yFinURL2 + "/v1/test/getcrumb"
	cookieURL  = "https://fc.yahoo.com"
	consentURL = "https://guce.yahoo.com/consent"
	userAgent  = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:109.0) Gecko/20100101 Firefox/113.0"
)

var (
	// LogLevel is the logging level for this library.
	// 0: no logging
	// 1: errors only
	// 2: errors + informational (default)
	// 3: errors + informational + debug
	LogLevel = 0

	// Logger controls how this library performs logging at a package level. It is useful
	// to customize if you need it prefixed for your application to meet other
	// requirements
	Logger Printfer

	// Private vars.
	// -------------

	httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	backends   Backends
)

// SupportedBackend is an enumeration of supported api endpoints.
type SupportedBackend string

// Backends are currently supported endpoints.
type Backends struct {
	YFin, Bats Backend
	mu         sync.RWMutex
}

// BackendConfiguration is the internal implementation for making HTTP calls.
type BackendConfiguration struct {
	Type       SupportedBackend
	URL        string
	HTTPClient *http.Client
}

// AuthStrategy represents the authentication strategy being used.
type AuthStrategy string

const (
	// BasicStrategy uses fc.yahoo.com for cookies
	BasicStrategy AuthStrategy = "basic"
	// CSRFStrategy uses the consent form flow
	CSRFStrategy AuthStrategy = "csrf"
)

// yahooConfiguration is a specialization that includes a crumb and cookies for the yahoo API.
type yahooConfiguration struct {
	BackendConfiguration
	expiry      time.Time
	cookies     string
	crumb       string
	strategy    AuthStrategy
	failedCount int
	mu          sync.Mutex
}

// Backend is an interface for making calls against an api service.
// This interface exists to enable mocking during testing if needed.
type Backend interface {
	Call(path string, body *form.Values, ctx *context.Context, v interface{}) error
}

// SetHTTPClient overrides the default HTTP client.
// This is useful if you're running in a Google AppEngine environment
// where http.DefaultClient is not available.
func SetHTTPClient(client *http.Client) {
	httpClient = client
}

// NewBackends creates a new set of backends with the given HTTP client. You
// should only need to use this for testing purposes or on App Engine.
func NewBackends(httpClient *http.Client) *Backends {
	return &Backends{
		YFin: &yahooConfiguration{
			BackendConfiguration: BackendConfiguration{YFinBackend, YFinURL, httpClient},
			expiry:               time.Time{},
			cookies:              "",
			crumb:                "",
			strategy:             BasicStrategy,
		},
		Bats: &BackendConfiguration{
			BATSBackend, BATSURL, httpClient,
		},
	}
}

// GetBackend returns the currently used backend in the binding.
func GetBackend(backend SupportedBackend) Backend {
	switch backend {
	case YFinBackend:
		backends.mu.RLock()
		ret := backends.YFin
		backends.mu.RUnlock()
		if ret != nil {
			return ret
		}
		backends.mu.Lock()
		defer backends.mu.Unlock()
		backends.YFin = &yahooConfiguration{
			BackendConfiguration: BackendConfiguration{YFinBackend, YFinURL, httpClient},
			expiry:               time.Time{},
			cookies:              "",
			crumb:                "",
			strategy:             BasicStrategy,
		}
		return backends.YFin
	case BATSBackend:
		backends.mu.RLock()
		ret := backends.Bats
		backends.mu.RUnlock()
		if ret != nil {
			return ret
		}
		backends.mu.Lock()
		defer backends.mu.Unlock()
		backends.Bats = &BackendConfiguration{backend, batsURL, httpClient}
		return backends.Bats
	}

	return nil
}

// SetBackend sets the backend used in the binding.
func SetBackend(backend SupportedBackend, b Backend) {
	switch backend {
	case YFinBackend:
		backends.YFin = b
	case BATSBackend:
		backends.Bats = b
	}
}

// fetchCookiesBasic implements the basic strategy to fetch cookies from fc.yahoo.com.
// This is the primary method used by yfinance.
func fetchCookiesBasic(client *http.Client) (string, time.Time, error) {
	request, err := http.NewRequest("GET", cookieURL, nil)
	if err != nil {
		return "", time.Time{}, err
	}

	request.Header = http.Header{
		"Accept":                    {"*/*"},
		"Accept-Encoding":           {"gzip, deflate, br"},
		"Accept-Language":           {"en-US,en;q=0.5"},
		"Connection":                {"keep-alive"},
		"Host":                      {"fc.yahoo.com"},
		"Sec-Fetch-Dest":            {"document"},
		"Sec-Fetch-Mode":            {"navigate"},
		"Sec-Fetch-Site":            {"none"},
		"Sec-Fetch-User":            {"?1"},
		"TE":                        {"trailers"},
		"Upgrade-Insecure-Requests": {"1"},
		"User-Agent":                {userAgent},
	}

	response, err := client.Do(request)
	if err != nil {
		if LogLevel > 0 {
			Logger.Printf("Error fetching cookies from fc.yahoo.com: %v\n", err)
		}
		return "", time.Time{}, err
	}
	defer response.Body.Close()

	var result string
	expiry := time.Now().AddDate(10, 0, 0) // Default to 10 years in the future

	for _, cookie := range response.Cookies() {
		if cookie.MaxAge <= 0 {
			continue
		}

		cookieExpiry := time.Now().Add(time.Duration(cookie.MaxAge) * time.Second)

		if cookie.Name != "AS" { // Skip the AS cookie
			result += cookie.Name + "=" + cookie.Value + "; "
			if cookie.Expires.Before(cookieExpiry) {
				expiry = cookieExpiry
			}
		}
	}

	result = strings.TrimSuffix(result, "; ")

	if LogLevel > 2 {
		Logger.Printf("Fetched cookies (length: %d, expiry: %v)\n", len(result), expiry)
	}

	return result, expiry, nil
}

// fetchCookiesCSRF implements the fallback strategy using the consent form.
// This is more complex but can work when the basic strategy fails.
func fetchCookiesCSRF(client *http.Client) (string, time.Time, error) {
	// First, visit the consent page
	request, err := http.NewRequest("GET", consentURL, nil)
	if err != nil {
		return "", time.Time{}, err
	}

	request.Header = http.Header{
		"Accept":          {"text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"},
		"Accept-Encoding": {"gzip, deflate, br"},
		"Accept-Language": {"en-US,en;q=0.5"},
		"Connection":      {"keep-alive"},
		"Host":            {"guce.yahoo.com"},
		"User-Agent":      {userAgent},
	}

	response, err := client.Do(request)
	if err != nil {
		if LogLevel > 0 {
			Logger.Printf("Error fetching consent page: %v\n", err)
		}
		return "", time.Time{}, err
	}
	defer response.Body.Close()

	// Extract cookies from response
	var result string
	expiry := time.Now().AddDate(10, 0, 0)

	for _, cookie := range response.Cookies() {
		if cookie.MaxAge <= 0 {
			continue
		}

		cookieExpiry := time.Now().Add(time.Duration(cookie.MaxAge) * time.Second)

		if cookie.Name != "AS" {
			result += cookie.Name + "=" + cookie.Value + "; "
			if cookie.Expires.Before(cookieExpiry) {
				expiry = cookieExpiry
			}
		}
	}

	result = strings.TrimSuffix(result, "; ")

	if LogLevel > 2 {
		Logger.Printf("Fetched cookies via CSRF (length: %d)\n", len(result))
	}

	return result, expiry, nil
}

// fetchCrumb fetches the crumb token using the given cookies.
// Uses query1 or query2 depending on the strategy.
func fetchCrumb(client *http.Client, cookies string, strategy AuthStrategy) (string, error) {
	crumbURL := crumbURL1
	if strategy == CSRFStrategy {
		crumbURL = crumbURL2
	}

	request, err := http.NewRequest("GET", crumbURL, nil)
	if err != nil {
		return "", err
	}

	host := "query1.finance.yahoo.com"
	if strategy == CSRFStrategy {
		host = "query2.finance.yahoo.com"
	}

	request.Header = http.Header{
		"Accept":          {"*/*"},
		"Accept-Encoding": {"gzip, deflate, br"},
		"Accept-Language": {"en-US,en;q=0.5"},
		"Connection":      {"keep-alive"},
		"Content-Type":    {"text/plain"},
		"Cookie":          {cookies},
		"Host":            {host},
		"Sec-Fetch-Dest":  {"empty"},
		"Sec-Fetch-Mode":  {"cors"},
		"Sec-Fetch-Site":  {"same-site"},
		"TE":              {"trailers"},
		"User-Agent":      {userAgent},
	}

	response, err := client.Do(request)
	if err != nil {
		if LogLevel > 0 {
			Logger.Printf("Error fetching crumb: %v\n", err)
		}
		return "", err
	}
	defer response.Body.Close()

	if response.StatusCode == 429 {
		if LogLevel > 0 {
			Logger.Printf("Rate limited when fetching crumb\n")
		}
		return "", CreateRemoteErrorS("rate limited")
	}

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}

	crumb := strings.TrimSpace(string(body))

	// Check if we got HTML instead of a crumb
	if strings.Contains(crumb, "<html>") || strings.Contains(crumb, "<!DOCTYPE") {
		if LogLevel > 0 {
			Logger.Printf("Received HTML instead of crumb\n")
		}
		return "", CreateRemoteErrorS("received html instead of crumb")
	}

	if crumb == "" {
		if LogLevel > 0 {
			Logger.Printf("Received empty crumb\n")
		}
		return "", CreateRemoteErrorS("received empty crumb")
	}

	if LogLevel > 2 {
		Logger.Printf("Fetched crumb: %s\n", crumb)
	}

	return crumb, nil
}

// refreshCrumbBasic implements the basic strategy to refresh cookies and crumb.
func (s *yahooConfiguration) refreshCrumbBasic() error {
	cookies, expiry, err := fetchCookiesBasic(s.HTTPClient)
	if err != nil {
		return err
	}

	crumb, err := fetchCrumb(s.HTTPClient, cookies, BasicStrategy)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.crumb = crumb
	s.expiry = expiry
	s.cookies = cookies
	s.strategy = BasicStrategy
	s.mu.Unlock()

	return nil
}

// refreshCrumbCSRF implements the fallback strategy to refresh cookies and crumb.
func (s *yahooConfiguration) refreshCrumbCSRF() error {
	cookies, expiry, err := fetchCookiesCSRF(s.HTTPClient)
	if err != nil {
		return err
	}

	crumb, err := fetchCrumb(s.HTTPClient, cookies, CSRFStrategy)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.crumb = crumb
	s.expiry = expiry
	s.cookies = cookies
	s.strategy = CSRFStrategy
	s.mu.Unlock()

	return nil
}

// refreshCrumb attempts to refresh cookies and crumb using the current strategy,
// with automatic fallback to the other strategy if it fails.
func (s *yahooConfiguration) refreshCrumb() error {
	s.mu.Lock()
	currentStrategy := s.strategy
	s.mu.Unlock()

	var err error

	// Try the current strategy first
	if currentStrategy == CSRFStrategy {
		err = s.refreshCrumbCSRF()
		if err == nil {
			return nil
		}
		if LogLevel > 1 {
			Logger.Printf("CSRF strategy failed, trying basic strategy: %v\n", err)
		}
		err = s.refreshCrumbBasic()
	} else {
		err = s.refreshCrumbBasic()
		if err == nil {
			return nil
		}
		if LogLevel > 1 {
			Logger.Printf("Basic strategy failed, trying CSRF strategy: %v\n", err)
		}
		err = s.refreshCrumbCSRF()
	}

	return err
}

// Call is the Backend.Call implementation for invoking market data APIs, using the Yahoo specialization.
func (s *yahooConfiguration) Call(path string, form *form.Values, ctx *context.Context, v interface{}) error {
	s.mu.Lock()
	needsRefresh := s.expiry.Before(time.Now()) || s.crumb == ""
	s.mu.Unlock()

	// Check if cookies have expired or crumb is missing.
	if needsRefresh {
		// Refresh cookies and crumb.
		err := s.refreshCrumb()
		if err != nil {
			if LogLevel > 0 {
				Logger.Printf("Failed to refresh crumb: %v\n", err)
			}
			return err
		}
	}

	// Build request with crumb
	s.mu.Lock()
	crumb := s.crumb
	cookies := s.cookies
	s.mu.Unlock()

	if crumb != "" {
		form.Add("crumb", crumb)
	}

	if form != nil && !form.Empty() {
		path += "?" + form.Encode()
	}

	req, err := s.newRequest("GET", path, ctx)
	if err != nil {
		return err
	}

	// Override cookie header with current cookies
	req.Header.Set("Cookie", cookies)

	if err := s.do(req, v); err != nil {
		// If we get a 4xx error, try refreshing and retrying once
		errStr := err.Error()
		if strings.Contains(errStr, "remote-error") && s.failedCount == 0 {
			s.failedCount++
			if LogLevel > 1 {
				Logger.Printf("Request failed, attempting to refresh and retry\n")
			}
			refreshErr := s.refreshCrumb()
			if refreshErr != nil {
				s.failedCount = 0
				return err
			}

			// Retry the request with new cookies/crumb
			s.mu.Lock()
			crumb = s.crumb
			cookies = s.cookies
			s.mu.Unlock()

			if form != nil && !form.Empty() {
				path = path[:strings.Index(path, "?")] + "?" + form.Encode()
			}

			req, err := s.newRequest("GET", path, ctx)
			if err != nil {
				s.failedCount = 0
				return err
			}
			req.Header.Set("Cookie", cookies)

			retryErr := s.do(req, v)
			s.failedCount = 0
			return retryErr
		}
		s.failedCount = 0
		return err
	}

	s.failedCount = 0
	return nil
}

// Call is the Backend.Call implementation for invoking market data APIs.
func (s *BackendConfiguration) Call(path string, form *form.Values, ctx *context.Context, v interface{}) error {

	if form != nil && !form.Empty() {
		path += "?" + form.Encode()
	}

	req, err := s.newRequest("GET", path, ctx)
	if err != nil {
		return err
	}

	if err := s.do(req, v); err != nil {
		return err
	}

	return nil
}

func (s *yahooConfiguration) newRequest(method, path string, ctx *context.Context) (*http.Request, error) {
	req, err := s.BackendConfiguration.newRequest(method, path, ctx)

	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	cookies := s.cookies
	strategy := s.strategy
	s.mu.Unlock()

	host := "query1.finance.yahoo.com"
	if strategy == CSRFStrategy {
		host = "query2.finance.yahoo.com"
	}

	req.Header = http.Header{
		"Accept":          {"*/*"},
		"Accept-Language": {"en-US,en;q=0.5"},
		"Connection":      {"keep-alive"},
		"Content-Type":    {"application/json"},
		"Cookie":          {cookies},
		"Host":            {host},
		"Origin":          {"https://finance.yahoo.com"},
		"Referer":         {"https://finance.yahoo.com"},
		"Sec-Fetch-Dest":  {"empty"},
		"Sec-Fetch-Mode":  {"cors"},
		"Sec-Fetch-Site":  {"same-site"},
		"TE":              {"trailers"},
		"User-Agent":      {userAgent},
	}

	return req, nil
}

func (s *BackendConfiguration) newRequest(method, path string, ctx *context.Context) (*http.Request, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	path = s.URL + path

	req, err := http.NewRequest(method, path, nil)
	if err != nil {
		if LogLevel > 0 {
			Logger.Printf("Cannot create api request: %v\n", err)
		}
		return nil, err
	}
	if ctx != nil {
		req = req.WithContext(*ctx)
	}

	return req, nil
}

// do is used by Call to execute an API request and parse the response. It uses
// the backend's HTTP client to execute the request and unmarshal the response
// into v. It also handles unmarshaling errors returned by the API.
func (s *BackendConfiguration) do(req *http.Request, v interface{}) error {
	if LogLevel > 1 {
		Logger.Printf("Requesting %v %v%v\n", req.Method, req.URL.Host, req.URL.Path)
	}

	start := time.Now()

	res, err := s.HTTPClient.Do(req)

	if LogLevel > 2 {
		Logger.Printf("Completed in %v\n", time.Since(start))
	}

	if err != nil {
		if LogLevel > 0 {
			Logger.Printf("Request to api failed: %v\n", err)
		}
		return err
	}
	defer res.Body.Close()

	resBody, err := io.ReadAll(res.Body)
	if err != nil {
		if LogLevel > 0 {
			Logger.Printf("Cannot parse response: %v\n", err)
		}
		return err
	}

	if res.StatusCode >= 400 {
		if LogLevel > 0 {
			Logger.Printf("API error: %q\n", resBody)
		}
		return CreateRemoteErrorS("error response recieved from upstream api")
	}

	if LogLevel > 2 {
		Logger.Printf("API response: %q\n", resBody)
	}

	if v != nil {
		return json.Unmarshal(resBody, v)
	}

	return nil
}
