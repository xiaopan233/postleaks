package postman

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xiaopan233/postleaks/go/internal/results"
)

func testClient(server *httptest.Server, log io.Writer) *Client {
	c := New(log, false)
	c.BaseURL = server.URL
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func TestPaginationLimits(t *testing.T) {
	for _, tc := range []struct{ total, pages int }{{0, 1}, {25, 1}, {26, 2}, {50, 2}, {225, 9}, {1000, 9}} {
		t.Run(fmt.Sprint(tc.total), func(t *testing.T) {
			var offsets []int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/_api/ws/proxy" || r.Method != "POST" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
				}
				var body struct {
					Service string `json:"service"`
					Method  string `json:"method"`
					Path    string `json:"path"`
					Body    struct {
						Indices []string `json:"queryIndices"`
						Text    string   `json:"queryText"`
						Size    int      `json:"size"`
						From    int      `json:"from"`
						Origin  string   `json:"requestOrigin"`
						Merge   string   `json:"mergeEntities"`
						Nested  string   `json:"nonNestedRequests"`
					} `json:"body"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.Service != "search" || body.Method != "POST" || body.Path != "/search-all" || body.Body.Size != 25 || body.Body.Text != "example.com" || body.Body.Merge != "true" || body.Body.Nested != "true" || body.Body.Origin != "srp" || !reflect.DeepEqual(body.Body.Indices, []string{"runtime.request"}) {
					t.Errorf("wrong search payload: %+v", body)
				}
				for name, value := range map[string]string{"User-Agent": UserAgent, "Accept": "application/json, text/plain, */*", "Referer": Host + "/", "Origin": Host, "Content-Type": "application/json"} {
					if r.Header.Get(name) != value {
						t.Errorf("%s = %q, want %q", name, r.Header.Get(name), value)
					}
				}
				offsets = append(offsets, body.Body.From)
				fmt.Fprintf(w, `{"meta":{"total":{"request":%d}},"data":[]}`, tc.total)
			}))
			defer server.Close()
			got, err := testClient(server, io.Discard).Search(context.Background(), "example.com", Options{})
			if err != nil || len(got) != 0 {
				t.Fatalf("results=%v, error=%v", got, err)
			}
			if len(offsets) != tc.pages {
				t.Fatalf("offsets=%v, want %d pages", offsets, tc.pages)
			}
			for i, offset := range offsets {
				if offset != i*25 || offset > 200 {
					t.Fatalf("invalid offsets %v", offsets)
				}
			}
		})
	}
}

func TestWorkspaceExtensionDedupAndDebug(t *testing.T) {
	counts := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counts[r.URL.RequestURI()]++
		switch r.URL.Path {
		case "/_api/ws/proxy":
			io.WriteString(w, `{"meta":{"total":{"request":26}},"data":[{"document":{"id":"r1","workspaces":[{"id":"w1"},{"id":"w1"},{"id":"w2"},{"id":"w3"}]}},{"document":{"id":"r1","workspaces":[]}}]}`)
		case "/_api/list/collection":
			if r.Method != "POST" {
				t.Error("workspace request must be POST")
			}
			switch r.URL.Query().Get("workspace") {
			case "w1":
				io.WriteString(w, `{"data":[{"requests":[{"id":"r1"},{"id":"r2"}]},{"requests":[{"id":"r2"}]}]}`)
			case "w2":
				io.WriteString(w, `{"data":null}`)
			case "w3":
				w.WriteHeader(404)
			}
		case "/_api/request/r1", "/_api/request/r2":
			fmt.Fprintf(w, `{"data":{"id":%q,"url":"https://example.com/api","method":"GET","extra":"ignore"}}`, strings.TrimPrefix(r.URL.Path, "/_api/request/"))
		default:
			t.Errorf("unexpected URL %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	var log bytes.Buffer
	c := testClient(server, &log)
	c.Debug = true
	got, err := c.Search(context.Background(), "example.com", Options{ExtendWorkspaces: true})
	if err != nil || len(got) != 2 || got[0].ID() != "r1" || got[1].ID() != "r2" {
		t.Fatalf("results=%v, error=%v", got, err)
	}
	for path, count := range counts {
		if strings.HasPrefix(path, "/_api/request/") || strings.HasPrefix(path, "/_api/list/") {
			if count != 1 {
				t.Errorf("%s fetched %d times", path, count)
			}
		}
	}
	for _, text := range []string{"REQUEST SENT", "RESPONSE RECEIVED", "queryText", "Headers received", "https://example.com/api", "Invalid collection response", "HTTP 404"} {
		if !strings.Contains(log.String(), text) {
			t.Errorf("missing %q in log", text)
		}
	}
}

func TestHTTPRetries(t *testing.T) {
	for _, status := range []int{429, 500, 502, 503, 504, 404, 501} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			attempts := 0
			var bodies []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				body, _ := io.ReadAll(r.Body)
				bodies = append(bodies, string(body))
				w.Header().Set("Retry-After", "99")
				w.WriteHeader(status)
				io.WriteString(w, "transient response")
			}))
			defer server.Close()
			var log bytes.Buffer
			c := testClient(server, &log)
			var delays []time.Duration
			c.Sleep = func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil }
			data, code, err := c.request(context.Background(), "POST", "/test", map[string]string{"key": "value"})
			wantAttempts := 1
			if retryable(status) {
				wantAttempts = 3
			}
			if err != nil || code != status || string(data) != "transient response" || attempts != wantAttempts {
				t.Fatalf("attempts=%d code=%d data=%s error=%v", attempts, code, data, err)
			}
			for _, body := range bodies {
				if body != `{"key":"value"}` {
					t.Errorf("request body lost during retry: %s", body)
				}
			}
			for _, delay := range delays {
				if delay != 5*time.Second {
					t.Errorf("uncapped retry: %v", delays)
				}
			}
			if wantAttempts == 3 && (!strings.Contains(log.String(), "attempt 1/3") || !strings.Contains(log.String(), "attempt 2/3") || !strings.Contains(log.String(), "retrying in 5s")) {
				t.Errorf("retry progress missing: %s", &log)
			}
		})
	}
	for _, tc := range []struct {
		header string
		want   time.Duration
	}{{"", 2 * time.Second}, {"garbage", 2 * time.Second}, {"NaN", 2 * time.Second}, {"-1", 2 * time.Second}, {"0.25", 250 * time.Millisecond}, {"0", 0}} {
		if got := retryDelay(tc.header, 2); got != tc.want {
			t.Errorf("Retry-After %q: %v, want %v", tc.header, got, tc.want)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestNetworkFailureAndCancellation(t *testing.T) {
	var log bytes.Buffer
	c := New(&log, false)
	attempts := 0
	c.HTTP.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return nil, errors.New("synthetic connection failure")
	})
	var delays []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil }
	if _, _, err := c.request(context.Background(), "GET", "/test", nil); err == nil || attempts != 3 || !reflect.DeepEqual(delays, []time.Duration{time.Second, 2 * time.Second}) {
		t.Fatalf("attempts=%d delays=%v error=%v", attempts, delays, err)
	}
	if !strings.Contains(log.String(), "synthetic connection failure") || !strings.Contains(log.String(), "attempt 2/3") {
		t.Fatalf("network retry progress missing: %s", &log)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.Sleep = func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() }
	attempts = 0
	if _, _, err := c.request(ctx, "GET", "/test", nil); !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("attempts=%d error=%v", attempts, err)
	}
	if err := sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleep ignored cancellation: %v", err)
	}
}

func TestResultsStreamBeforeNextFetchAndCallbackFailure(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(fmt.Sprintf("callback-error=%v", stop), func(t *testing.T) {
			var log bytes.Buffer
			c := New(&log, false)
			var delivered []string
			var fetched []string
			callbackErr := errors.New("ordinary output failure")
			c.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				body := `{"meta":{"total":{"request":2}},"data":[{"document":{"id":"a"}},{"document":{"id":"b"}}]}`
				if r.Method == "GET" {
					id := strings.TrimPrefix(r.URL.Path, "/_api/request/")
					fetched = append(fetched, id)
					if !strings.Contains(log.String(), fmt.Sprintf("Fetching request %d/2 (ID:%s)", len(fetched), id)) {
						t.Fatal("progress must be logged before starting the HTTP request")
					}
					if id == "b" && !reflect.DeepEqual(delivered, []string{"a"}) {
						t.Fatalf("second request started before the first result was delivered: %v", delivered)
					}
					body = fmt.Sprintf(`{"data":{"id":%q,"url":"https://example.com","method":"GET"}}`, id)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
			})
			found, err := c.Search(context.Background(), "example.com", Options{OnResult: func(request results.Request) error {
				delivered = append(delivered, request.ID())
				if stop {
					return callbackErr
				}
				return nil
			}})
			want := []string{"a", "b"}
			if stop {
				want = []string{"a"}
				if !errors.Is(err, callbackErr) {
					t.Fatalf("callback error lost: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if len(found) != len(want) || !reflect.DeepEqual(fetched, want) || !reflect.DeepEqual(delivered, want) {
				t.Fatalf("found=%v fetched=%v delivered=%v, want=%v", found, fetched, delivered, want)
			}
			if !strings.Contains(log.String(), "Found 2 unique request IDs; timeout 30s per attempt") {
				t.Fatalf("total count and timeout missing: %s", &log)
			}
		})
	}
}

func TestMalformedSearchAndPartialPages(t *testing.T) {
	for _, body := range []string{`not json`, `{}`, `{"meta":{"total":{"request":0}}}`, `{"meta":{"total":{"request":-1}},"data":[]}`, `{"meta":{"total":{"request":"1"}},"data":[]}`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
			defer server.Close()
			if _, err := testClient(server, io.Discard).Search(context.Background(), "test", Options{}); err == nil {
				t.Fatal("expected search error")
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			io.WriteString(w, `{"data":{"id":"r1","url":"https://example.com","method":"GET"}}`)
			return
		}
		var body struct {
			Body struct {
				From int `json:"from"`
			} `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Body.From > 0 {
			w.WriteHeader(503)
			return
		}
		io.WriteString(w, `{"meta":{"total":{"request":26}},"data":[{"document":{"id":"r1","workspaces":[]}}]}`)
	}))
	defer server.Close()
	var log bytes.Buffer
	got, err := testClient(server, &log).Search(context.Background(), "test", Options{})
	if err != nil || len(got) != 1 || !strings.Contains(log.String(), "Unable to fetch search page") {
		t.Fatalf("results=%v error=%v log=%s", got, err, &log)
	}
}

func TestSkippedRequestDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_api/ws/proxy":
			io.WriteString(w, `{"meta":{"total":{"request":5}},"data":[{"document":{"id":"ok"}},{"document":{"id":"404"}},{"document":{"id":"json"}},{"document":{"id":"data"}},{"document":{"id":"id"}}]}`)
		case "/_api/request/ok":
			io.WriteString(w, `{"data":{"id":"ok","url":"https://example.com","method":"GET"}}`)
		case "/_api/request/404":
			w.WriteHeader(404)
		case "/_api/request/json":
			io.WriteString(w, `{"data":`)
		case "/_api/request/data":
			io.WriteString(w, `{"data":[]}`)
		case "/_api/request/id":
			io.WriteString(w, `{"data":{"url":"https://example.com"}}`)
		}
	}))
	defer server.Close()
	var log bytes.Buffer
	got, err := testClient(server, &log).Search(context.Background(), "test", Options{})
	if err != nil || len(got) != 1 || !strings.Contains(log.String(), "404=1, invalid_json=1, missing_data=1, missing_id=1") {
		t.Fatalf("results=%v error=%v log=%s", got, err, &log)
	}
}
