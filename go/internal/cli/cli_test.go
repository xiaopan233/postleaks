package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xiaopan233/postleaks/go/internal/postman"
	"github.com/xiaopan233/postleaks/go/internal/results"
)

func serverDependencies(server *httptest.Server) dependencies {
	return dependencies{
		newClient: func(log io.Writer, debug bool) *postman.Client {
			client := postman.New(log, debug)
			client.BaseURL = server.URL
			client.Sleep = func(context.Context, time.Duration) error { return nil }
			return client
		},
		now: func() time.Time { return time.Unix(1234567890, 0) },
	}
}

func TestKeywordsAndArgumentErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "keywords.txt")
	if err := os.WriteFile(file, []byte(" example.com \r\n\nother.test\nexample.com\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := loadKeywords("example.com", file)
	if err != nil || !reflect.DeepEqual(got, []string{"example.com", "other.test"}) {
		t.Fatalf("keywords=%v, error=%v", got, err)
	}
	deps := dependencies{newClient: func(io.Writer, bool) *postman.Client {
		t.Fatal("invalid arguments must not reach the network")
		return nil
	}, now: time.Now}
	for _, tc := range []struct {
		args []string
		code int
	}{{nil, 2}, {[]string{"-k", " "}, 2}, {[]string{"--unknown"}, 2}, {[]string{"-k", "example.com", "extra"}, 2}, {[]string{"-kf", filepath.Join(t.TempDir(), "missing.txt")}, 1}, {[]string{"--help"}, 0}, {[]string{"-h"}, 0}} {
		var out, log bytes.Buffer
		if code := run(context.Background(), tc.args, &out, &log, deps); code != tc.code {
			t.Errorf("args=%v code=%d want=%d log=%s", tc.args, code, tc.code, &log)
		}
		if out.Len() != 0 {
			t.Errorf("arguments wrote to stdout: %s", &out)
		}
	}
}

func TestCLICombinedKeywordsRawAndLastResultWins(t *testing.T) {
	var keywords []string
	counts := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			var body struct {
				Body struct {
					Query string `json:"queryText"`
				} `json:"body"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			keywords = append(keywords, body.Body.Query)
			io.WriteString(w, `{"meta":{"total":{"request":2}},"data":[{"document":{"id":"b"}},{"document":{"id":"a"}}]}`)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/_api/request/")
		counts[id]++
		fmt.Fprintf(w, `{"data":{"id":%q,"url":"https://example.com/api","method":"GET","name":%q,"auth":null,"extra":"omit"}}`, id, fmt.Sprintf("普通说明 %d", counts[id]))
	}))
	defer server.Close()
	directory := t.TempDir()
	keywordFile := filepath.Join(directory, "keywords.txt")
	if err := os.WriteFile(keywordFile, []byte("example.com\n\nother.test\nother.test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(directory, "nested", "output.json")
	var out, log bytes.Buffer
	code := run(context.Background(), []string{"-k", "example.com", "-kf", keywordFile, "--include", "/API", "--exclude", "/private", "--raw", "--output", output}, &out, &log, serverDependencies(server))
	if code != 0 || !reflect.DeepEqual(keywords, []string{"example.com", "other.test"}) {
		t.Fatalf("code=%d keywords=%v log=%s", code, keywords, &log)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 || strings.Contains(out.String(), "\033") {
		t.Fatalf("invalid raw output: %s", &out)
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Errorf("invalid raw line: %s", line)
		}
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var saved []results.Request
	if err := json.Unmarshal(data, &saved); err != nil || len(saved) != 2 {
		t.Fatalf("export=%s error=%v", data, err)
	}
	for i, id := range []string{"a", "b"} {
		if saved[i].ID() != id || saved[i].String("name") != "普通说明 2" || string(saved[i]["auth"]) != "null" {
			t.Errorf("invalid deduplication: %v", saved[i])
		}
		if _, exists := saved[i]["extra"]; exists {
			t.Fatal("unexpected exported field")
		}
	}
	if !strings.Contains(log.String(), "Saved 2 unique results") {
		t.Fatalf("missing completion status: %s", &log)
	}
}

func TestCLIEmptyAndPartialResults(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			searches := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					searches++
					if partial && searches > 1 {
						w.WriteHeader(403)
						return
					}
					if partial {
						io.WriteString(w, `{"meta":{"total":{"request":1}},"data":[{"document":{"id":"a"}}]}`)
					} else {
						io.WriteString(w, `{"meta":{"total":{"request":0}},"data":[]}`)
					}
					return
				}
				io.WriteString(w, `{"data":{"id":"a","url":"https://example.com","method":"GET"}}`)
			}))
			defer server.Close()
			directory := t.TempDir()
			keywords := filepath.Join(directory, "keywords.txt")
			if err := os.WriteFile(keywords, []byte("first\nsecond\n"), 0600); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(directory, "output.json")
			var out, log bytes.Buffer
			code := run(context.Background(), []string{"-kf", keywords, "--output", output}, &out, &log, serverDependencies(server))
			wantCode, wantCount := 0, 0
			if partial {
				wantCode, wantCount = 1, 1
			}
			data, err := os.ReadFile(output)
			var saved []results.Request
			if err != nil || json.Unmarshal(data, &saved) != nil || code != wantCode || len(saved) != wantCount {
				t.Fatalf("code=%d export=%s error=%v log=%s", code, data, err, &log)
			}
			if !partial && strings.TrimSpace(string(data)) != "[]" {
				t.Fatalf("empty export=%s", data)
			}
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("synthetic output failure") }

func TestCLIOutputFailurePreservesFetchedResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			io.WriteString(w, `{"meta":{"total":{"request":2}},"data":[{"document":{"id":"a"}},{"document":{"id":"b"}}]}`)
			return
		}
		fmt.Fprintf(w, `{"data":{"id":%q,"url":"https://example.com","method":"GET"}}`, strings.TrimPrefix(r.URL.Path, "/_api/request/"))
	}))
	defer server.Close()
	output := filepath.Join(t.TempDir(), "output.json")
	var log bytes.Buffer
	code := run(context.Background(), []string{"-k", "example.com", "--raw", "--output", output}, failingWriter{}, &log, serverDependencies(server))
	data, err := os.ReadFile(output)
	var saved []results.Request
	if code != 1 || err != nil || json.Unmarshal(data, &saved) != nil || len(saved) != 1 || saved[0].ID() != "a" {
		t.Fatalf("code=%d export=%s error=%v log=%s", code, data, err, &log)
	}
}

func TestDisplayEncodedDataAndNulls(t *testing.T) {
	var request results.Request
	if err := json.Unmarshal([]byte(`{"id":"a","method":"GET","url":null,"auth":null,"headerData":[{"key":"Accept","value":"application/json"}],"queryParams":[{"key":"page","value":"1"}],"data":["[{\"key\":\"title\",\"value\":\"普通说明\"}]",null,"[broken"]}`), &request); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := display(&out, request, false, false); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"(ID:a) GET: null", "Headers", "Query parameters", "普通说明"} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("missing %q in %s", text, &out)
		}
	}
	if strings.Contains(out.String(), "Authentication items") {
		t.Fatal("null auth should be omitted from human display")
	}
}
