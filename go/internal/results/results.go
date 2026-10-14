package results

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Request retains JSON fields exactly, including absent fields and explicit nulls.
type Request map[string]json.RawMessage

var fields = []string{"id", "url", "method", "auth", "queryParams", "description", "name", "events", "data", "headerData"}

type Filter struct {
	Keyword string
	Strict  bool
	Include *string
	Exclude *string
}

func Select(data map[string]json.RawMessage, filter Filter) (Request, bool) {
	urlJSON, hasURL := data["url"]
	if !hasURL {
		return nil, false
	}
	if filter.Strict || filter.Include != nil || filter.Exclude != nil {
		var url string
		if err := json.Unmarshal(urlJSON, &url); err != nil || len(url) == 0 {
			return nil, false
		}
		lower := strings.ToLower(url)
		if filter.Include != nil && !strings.Contains(lower, strings.ToLower(*filter.Include)) {
			return nil, false
		}
		if filter.Exclude != nil && strings.Contains(lower, strings.ToLower(*filter.Exclude)) {
			return nil, false
		}
		if filter.Strict && !strings.Contains(lower, strings.ToLower(filter.Keyword)) {
			return nil, false
		}
	}
	r := make(Request)
	for _, field := range fields {
		if value, exists := data[field]; exists {
			r[field] = value
		}
	}
	if r.ID() == "" {
		return nil, false
	}
	return r, true
}

func (r Request) ID() string { return r.String("id") }

func (r Request) String(key string) string {
	var value string
	_ = json.Unmarshal(r[key], &value)
	return value
}

func Sorted(byID map[string]Request) []Request {
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	requests := make([]Request, 0, len(ids))
	for _, id := range ids {
		requests = append(requests, byID[id])
	}
	return requests
}

func Store(requests []Request, path string) error {
	if requests == nil {
		requests = []Request{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("open output: %w", err)
	}
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(requests); err != nil {
		_ = f.Close()
		return fmt.Errorf("write output: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close output: %w", err)
	}
	return nil
}
