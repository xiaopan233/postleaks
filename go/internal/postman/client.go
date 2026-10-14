package postman

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/cookiejar"
	"net/http/httputil"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xiaopan233/postleaks/go/internal/results"
)

const Host = "https://www.postman.com"
const UserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/137.0.0.0 Safari/537.36"

type Client struct {
	BaseURL string
	HTTP    *http.Client
	Sleep   func(context.Context, time.Duration) error
	Log     io.Writer
	Debug   bool
}

func New(log io.Writer, debug bool) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		BaseURL: Host,
		HTTP:    &http.Client{Timeout: 30 * time.Second, Jar: jar},
		Sleep:   sleep,
		Log:     log,
		Debug:   debug,
	}
}

func sleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) log(format string, args ...any) {
	if c.Log != nil {
		fmt.Fprintf(c.Log, format+"\n", args...)
	}
}

func retryable(status int) bool {
	switch status {
	case 429, 500, 502, 503, 504:
		return true
	}
	return false
}

func retryDelay(header string, attempt int) time.Duration {
	seconds, err := strconv.ParseFloat(header, 64)
	if header == "" || err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		return time.Duration(attempt) * time.Second
	}
	return time.Duration(math.Min(seconds, 5) * float64(time.Second))
}

func (c *Client) request(ctx context.Context, method, path string, payload any) ([]byte, int, error) {
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
	}
	for attempt := 1; attempt <= 3; attempt++ {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("User-Agent", UserAgent)
		req.Header.Set("Accept", "application/json, text/plain, */*")
		req.Header.Set("Referer", Host+"/")
		req.Header.Set("Origin", Host)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.Debug {
			dump, dumpErr := httputil.DumpRequestOut(req, true)
			if dumpErr == nil {
				c.log("\n%s\n[DEBUG] >>> REQUEST SENT (attempt %d)\n%s", strings.Repeat("=", 70), attempt, dump)
			}
		}
		start := time.Now()
		response, requestErr := c.HTTP.Do(req)
		var data []byte
		if requestErr == nil {
			data, requestErr = io.ReadAll(response.Body)
			_ = response.Body.Close()
			if c.Debug {
				c.log("[DEBUG] <<< RESPONSE RECEIVED\n Status: %s\n Elapsed: %s\n Headers received:", response.Status, time.Since(start))
				keys := make([]string, 0, len(response.Header))
				for key := range response.Header {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					c.log("   %s: %s", key, strings.Join(response.Header.Values(key), ", "))
				}
				c.log(" Body received:\n%s\n%s", data, strings.Repeat("=", 70))
			}
		} else if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		if requestErr != nil {
			if attempt == 3 {
				return nil, 0, fmt.Errorf("request failed: %w", requestErr)
			}
			delay := time.Duration(attempt) * time.Second
			c.log("[-] %s %s failed (attempt %d/3): %v; retrying in %s", method, path, attempt, requestErr, delay)
			if err := c.Sleep(ctx, delay); err != nil {
				return nil, 0, err
			}
			continue
		}
		if !retryable(response.StatusCode) || attempt == 3 {
			return data, response.StatusCode, nil
		}
		delay := retryDelay(response.Header.Get("Retry-After"), attempt)
		c.log("[-] %s %s returned HTTP %d (attempt %d/3); retrying in %s", method, path, response.StatusCode, attempt, delay)
		if err := c.Sleep(ctx, delay); err != nil {
			return nil, 0, err
		}
	}
	return nil, 0, fmt.Errorf("request attempts exhausted")
}

func searchBody(keyword string, offset int) any {
	return map[string]any{
		"service": "search", "method": "POST", "path": "/search-all",
		"body": map[string]any{
			"queryIndices": []string{"runtime.request"}, "queryText": keyword,
			"size": 25, "from": offset, "requestOrigin": "srp",
			"mergeEntities": "true", "nonNestedRequests": "true",
		},
	}
}

type document struct {
	ID         string `json:"id"`
	Workspaces []struct {
		ID string `json:"id"`
	} `json:"workspaces"`
}

type searchResponse struct {
	Meta struct {
		Total struct {
			Request *int `json:"request"`
		} `json:"total"`
	} `json:"meta"`
	Data *[]struct {
		Document document `json:"document"`
	} `json:"data"`
}

func (c *Client) searchPage(ctx context.Context, keyword string, offset int) (searchResponse, error) {
	var page searchResponse
	data, status, err := c.request(ctx, http.MethodPost, "/_api/ws/proxy", searchBody(keyword, offset))
	if err != nil {
		return page, err
	}
	if status != 200 {
		return page, fmt.Errorf("search offset %d: HTTP %d", offset, status)
	}
	if err := json.Unmarshal(data, &page); err != nil {
		return page, fmt.Errorf("search offset %d: invalid JSON: %w", offset, err)
	}
	if page.Data == nil || (offset == 0 && (page.Meta.Total.Request == nil || *page.Meta.Total.Request < 0)) {
		return page, fmt.Errorf("search offset %d: missing or invalid data/count", offset)
	}
	return page, nil
}

type Options struct {
	ExtendWorkspaces bool
	Filter           results.Filter
	// OnResult runs as soon as a request passes filtering, before fetching the
	// next request. A callback error stops the search and preserves fetched results.
	OnResult func(results.Request) error
}

func (c *Client) Search(ctx context.Context, keyword string, options Options) ([]results.Request, error) {
	c.log("[*] Looking for data in Postman.com\n[*] Searching for requests IDs")
	page, err := c.searchPage(ctx, keyword, 0)
	if err != nil {
		return nil, err
	}
	requestIDs := make(map[string]bool)
	workspaceIDs := make(map[string]bool)
	add := func(page searchResponse) {
		for _, hit := range *page.Data {
			if hit.Document.ID == "" {
				c.log("[-] Skipped search result without a request ID")
				continue
			}
			requestIDs[hit.Document.ID] = true
			if options.ExtendWorkspaces {
				for _, workspace := range hit.Document.Workspaces {
					if workspace.ID != "" {
						workspaceIDs[workspace.ID] = true
					}
				}
			}
		}
	}
	add(page)
	total := *page.Meta.Total.Request
	for offset := 25; offset < total && offset <= 200; offset += 25 {
		c.log("[*] Fetching search page at offset %d", offset)
		page, err := c.searchPage(ctx, keyword, offset)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			c.log("[-] Unable to fetch search page: %v", err)
			continue
		}
		add(page)
	}
	if options.ExtendWorkspaces {
		c.log("[*] Looking for requests IDs in collection of workspaces")
		workspaces := sortedKeys(workspaceIDs)
		for i, id := range workspaces {
			c.log("[*] Fetching workspace %d/%d (ID:%s)", i+1, len(workspaces), id)
			data, status, err := c.request(ctx, http.MethodPost, "/_api/list/collection?workspace="+url.QueryEscape(id), nil)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if err != nil || status != 200 {
				c.log("[-] Unable to fetch workspace %s: HTTP %d, error=%v", id, status, err)
				continue
			}
			var response struct {
				Data *[]struct {
					Requests []struct {
						ID string `json:"id"`
					} `json:"requests"`
				} `json:"data"`
			}
			if err := json.Unmarshal(data, &response); err != nil || response.Data == nil {
				c.log("[-] Invalid collection response for workspace %s", id)
				continue
			}
			for _, collection := range *response.Data {
				for _, request := range collection.Requests {
					if request.ID != "" {
						requestIDs[request.ID] = true
					}
				}
			}
		}
	}
	c.log("[*] Search for requests info in collection of requests")
	ids := sortedKeys(requestIDs)
	c.log("[*] Found %d unique request IDs; timeout %s per attempt, up to 3 attempts per request", len(ids), c.HTTP.Timeout)
	found := make([]results.Request, 0)
	failures := make(map[string]int)
	options.Filter.Keyword = keyword
	for i, id := range ids {
		c.log("[*] Fetching request %d/%d (ID:%s)", i+1, len(ids), id)
		data, status, err := c.request(ctx, http.MethodGet, "/_api/request/"+url.PathEscape(id), nil)
		if ctx.Err() != nil {
			return found, ctx.Err()
		}
		if err != nil {
			failures["request_error"]++
			c.log("[-] Unable to fetch request %s: %v", id, err)
			continue
		}
		if status != 200 {
			failures[strconv.Itoa(status)]++
			c.log("[-] Unable to fetch request %s: HTTP %d", id, status)
			continue
		}
		var response map[string]json.RawMessage
		if err := json.Unmarshal(data, &response); err != nil {
			failures["invalid_json"]++
			c.log("[-] Invalid JSON returned for request %s", id)
			continue
		}
		var details map[string]json.RawMessage
		if err := json.Unmarshal(response["data"], &details); err != nil || details == nil {
			failures["missing_data"]++
			c.log("[-] Response for request %s has no valid data field", id)
			continue
		}
		var detailID string
		if err := json.Unmarshal(details["id"], &detailID); err != nil || detailID == "" {
			failures["missing_id"]++
			c.log("[-] Response for request %s has no valid ID", id)
			continue
		}
		if request, ok := results.Select(details, options.Filter); ok {
			found = append(found, request)
			if options.OnResult != nil {
				if err := options.OnResult(request); err != nil {
					return found, err
				}
			}
		}
	}
	keys := make([]string, 0, len(failures))
	for key := range failures {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			parts = append(parts, fmt.Sprintf("%s=%d", key, failures[key]))
		}
		c.log("[-] Requests skipped: %s", strings.Join(parts, ", "))
	}
	return found, nil
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
