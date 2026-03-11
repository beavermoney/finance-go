package finance

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/beavermoney/finance-go/form"
	"github.com/stretchr/testify/assert"
)

func TestLogger(t *testing.T) {
	// Test default logger exists
	assert.NotNil(t, Logger)
}

func TestLogLevel(t *testing.T) {
	// Test default log level
	assert.Equal(t, 0, LogLevel)

	// Test setting log level
	LogLevel = 1
	assert.Equal(t, 1, LogLevel)
	LogLevel = 2
	assert.Equal(t, 2, LogLevel)
	LogLevel = 3
	assert.Equal(t, 3, LogLevel)
}

func TestSetHTTPClient(t *testing.T) {
	// Save original client
	originalClient := httpClient

	// Create a custom client
	customClient := &http.Client{Timeout: 30 * time.Second}
	SetHTTPClient(customClient)

	// Verify client was set
	assert.Equal(t, customClient, httpClient)

	// Restore original client
	httpClient = originalClient
}

func TestNewBackends(t *testing.T) {
	customClient := &http.Client{Timeout: 30 * time.Second}

	backends := NewBackends(customClient)

	assert.NotNil(t, backends)
	assert.NotNil(t, backends.YFin)
	assert.NotNil(t, backends.Bats)

	// Verify the HTTP client was set
	yfinConfig := backends.YFin.(*yahooConfiguration)
	assert.Equal(t, customClient, yfinConfig.HTTPClient)

	batsConfig := backends.Bats.(*BackendConfiguration)
	assert.Equal(t, customClient, batsConfig.HTTPClient)
}

func TestGetBackend(t *testing.T) {
	// Test YFin backend
	yfinBackend := GetBackend(YFinBackend)
	assert.NotNil(t, yfinBackend)
	assert.Equal(t, YFinBackend, yfinBackend.(*yahooConfiguration).Type)

	// Test BATS backend
	batsBackend := GetBackend(BATSBackend)
	assert.NotNil(t, batsBackend)
	assert.Equal(t, BATSBackend, batsBackend.(*BackendConfiguration).Type)

	// Test unknown backend (should return nil)
	unknownBackend := GetBackend(SupportedBackend("unknown"))
	assert.Nil(t, unknownBackend)
}

func TestSetBackend(t *testing.T) {
	// Save original backends
	originalYFin := backends.YFin
	originalBats := backends.Bats

	// Create custom backends
	customYFin := &yahooConfiguration{
		BackendConfiguration: BackendConfiguration{
			Type:       YFinBackend,
			URL:        "http://custom-yfin",
			HTTPClient: &http.Client{},
		},
	}

	customBats := &BackendConfiguration{
		Type:       BATSBackend,
		URL:        "http://custom-bats",
		HTTPClient: &http.Client{},
	}

	// Set custom backends
	SetBackend(YFinBackend, customYFin)
	SetBackend(BATSBackend, customBats)

	// Verify they were set
	assert.Equal(t, customYFin, backends.YFin)
	assert.Equal(t, customBats, backends.Bats)

	// Restore original backends
	backends.YFin = originalYFin
	backends.Bats = originalBats
}

func TestBackendConcurrency(t *testing.T) {
	// Test concurrent access to backends
	var wg sync.WaitGroup
	numGoroutines := 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(3)

		go func() {
			defer wg.Done()
			GetBackend(YFinBackend)
		}()

		go func() {
			defer wg.Done()
			GetBackend(BATSBackend)
		}()

		go func() {
			defer wg.Done()
			customBackend := &BackendConfiguration{
				Type:       BATSBackend,
				URL:        "http://test",
				HTTPClient: &http.Client{},
			}
			SetBackend(BATSBackend, customBackend)
		}()
	}

	wg.Wait()

	// Verify backends are still accessible
	yfinBackend := GetBackend(YFinBackend)
	assert.NotNil(t, yfinBackend)

	batsBackend := GetBackend(BATSBackend)
	assert.NotNil(t, batsBackend)
}

func TestBackendConfigurationNewRequest(t *testing.T) {
	config := &BackendConfiguration{
		Type:       YFinBackend,
		URL:        "http://test.com",
		HTTPClient: &http.Client{},
	}

	// Test with path starting with /
	req, err := config.newRequest("GET", "/api/test", nil)
	assert.NoError(t, err)
	assert.NotNil(t, req)
	assert.Equal(t, "http://test.com/api/test", req.URL.String())
	assert.Equal(t, "GET", req.Method)

	// Test with path not starting with /
	req2, err := config.newRequest("GET", "api/test", nil)
	assert.NoError(t, err)
	assert.NotNil(t, req2)
	assert.Equal(t, "http://test.com/api/test", req2.URL.String())

	// Test with context
	ctx := context.Background()
	req3, err := config.newRequest("POST", "/api/create", &ctx)
	assert.NoError(t, err)
	assert.NotNil(t, req3)
	assert.Equal(t, "POST", req3.Method)
	assert.NotNil(t, req3.Context())
}

func TestYahooConfigurationNewRequest(t *testing.T) {
	config := &yahooConfiguration{
		BackendConfiguration: BackendConfiguration{
			Type:       YFinBackend,
			URL:        "http://test.com",
			HTTPClient: &http.Client{},
		},
		cookies: "test-cookie=value",
		crumb:   "test-crumb",
	}

	req, err := config.newRequest("GET", "/api/test", nil)
	assert.NoError(t, err)
	assert.NotNil(t, req)

	// Check headers
	assert.Equal(t, "test-cookie=value", req.Header.Get("Cookie"))
	assert.Equal(t, "https://finance.yahoo.com", req.Header.Get("Referer"))
	assert.Equal(t, "https://finance.yahoo.com", req.Header.Get("Origin"))
	assert.Contains(t, req.Header.Get("User-Agent"), "Mozilla")
	assert.Contains(t, req.Header.Get("User-Agent"), "Firefox")
}

func TestBackendConfigurationDo(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"test": "data"}`))
	}))
	defer server.Close()

	config := &BackendConfiguration{
		Type:       YFinBackend,
		URL:        server.URL,
		HTTPClient: server.Client(),
	}

	req, _ := config.newRequest("GET", "/test", nil)
	var result map[string]interface{}
	err := config.do(req, &result)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "data", result["test"])
}

func TestBackendConfigurationDoError(t *testing.T) {
	config := &BackendConfiguration{
		Type:       YFinBackend,
		URL:        "http://invalid-domain-that-does-not-exist-12345.com",
		HTTPClient: &http.Client{Timeout: 1 * time.Second},
	}

	req, _ := config.newRequest("GET", "/test", nil)
	var result map[string]interface{}
	err := config.do(req, &result)

	assert.Error(t, err)
}

func TestBackendConfigurationDoHTTPError(t *testing.T) {
	// Create a test server that returns 404
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error": "not found"}`))
	}))
	defer server.Close()

	config := &BackendConfiguration{
		Type:       YFinBackend,
		URL:        server.URL,
		HTTPClient: server.Client(),
	}

	req, _ := config.newRequest("GET", "/test", nil)
	var result map[string]interface{}
	err := config.do(req, &result)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "remote-error")
}

func TestBackendConfigurationDoNilResponse(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"test": "data"}`))
	}))
	defer server.Close()

	config := &BackendConfiguration{
		Type:       YFinBackend,
		URL:        server.URL,
		HTTPClient: server.Client(),
	}

	req, _ := config.newRequest("GET", "/test", nil)
	err := config.do(req, nil) // Pass nil as response target

	assert.NoError(t, err)
}

func TestBackendConfigurationCall(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"test": "data"}`))
	}))
	defer server.Close()

	config := &BackendConfiguration{
		Type:       YFinBackend,
		URL:        server.URL,
		HTTPClient: server.Client(),
	}

	var result map[string]interface{}
	err := config.Call("/test", nil, nil, &result)

	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.Equal(t, "data", result["test"])
}

func TestBackendConfigurationCallWithForm(t *testing.T) {
	// Create a test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify query parameters
		assert.Equal(t, "value1", r.URL.Query().Get("param1"))
		assert.Equal(t, "value2", r.URL.Query().Get("param2"))

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"test": "data"}`))
	}))
	defer server.Close()

	config := &BackendConfiguration{
		Type:       YFinBackend,
		URL:        server.URL,
		HTTPClient: server.Client(),
	}

	formValues := &form.Values{}
	formValues.Add("param1", "value1")
	formValues.Add("param2", "value2")

	var result map[string]interface{}
	err := config.Call("/test", formValues, nil, &result)

	assert.NoError(t, err)
	assert.NotNil(t, result)
}

func TestConstants(t *testing.T) {
	assert.Equal(t, "yahoo", string(YFinBackend))
	assert.Equal(t, "https://query1.finance.yahoo.com", YFinURL)
	assert.Equal(t, "bats", string(BATSBackend))
	assert.Equal(t, "", BATSURL)
	assert.Equal(t, 80*time.Second, defaultHTTPTimeout)
}

func TestLoggerPrintf(t *testing.T) {
	// Test that Logger.Printf works
	var buf bytes.Buffer
	oldLogger := Logger
	defer func() { Logger = oldLogger }()

	Logger = &testLogger{&buf}

	LogLevel = 2
	Logger.Printf("Test message %s", "value")

	output := buf.String()
	assert.Contains(t, output, "Test message")
	assert.Contains(t, output, "value")
}

// testLogger is a simple logger for testing
type testLogger struct {
	io.Writer
}

func (l *testLogger) Printf(format string, v ...interface{}) {
	fmt.Fprintf(l.Writer, format, v...)
}
