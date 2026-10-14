package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/xiaopan233/postleaks/go/internal/detector"
	"github.com/xiaopan233/postleaks/go/internal/postman"
	"github.com/xiaopan233/postleaks/go/internal/results"
)

type dependencies struct {
	newClient func(io.Writer, bool) *postman.Client
	now       func() time.Time
}

func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return run(ctx, args, stdout, stderr, dependencies{newClient: postman.New, now: time.Now})
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies) int {
	flags := flag.NewFlagSet("postleaks", flag.ContinueOnError)
	flags.SetOutput(stderr)
	keyword := flags.String("k", "", "Keyword (Domain, company, etc.)")
	keywordFile := flags.String("kf", "", "File containing keywords (one per line)")
	extend := flags.Bool("extend-workspaces", false, "Extend search to workspaces linked to found requests (request consuming; possible false positives)")
	strict := flags.Bool("strict", false, "Only include results containing the keyword in the URL")
	include := flags.String("include", "", "URL should match this string")
	exclude := flags.String("exclude", "", "URL should not match this string")
	raw := flags.Bool("raw", false, "Display filtered results as JSON, one object per line")
	output := flags.String("output", "", "Store all results in one JSON file (default: results_<TIMESTAMP>.json)")
	debug := flags.Bool("debug", false, "Display full HTTP requests and responses on stderr")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Postleaks 🚀💧 Search for sensitive data in Postman public library.")
		fmt.Fprintln(stderr, "Usage: postleaks [-k KEYWORD] [-kf KEYWORD_FILE] [options]")
		fmt.Fprintln(stderr, "  -h, --help\n        Show this help message and exit")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "[-] Error: unexpected arguments: %s\n", strings.Join(flags.Args(), " "))
		return 2
	}
	keywords, err := loadKeywords(*keyword, *keywordFile)
	if err != nil {
		fmt.Fprintf(stderr, "[-] Error: %v\n", err)
		if *keywordFile != "" {
			return 1
		}
		flags.Usage()
		return 2
	}
	if len(keywords) == 0 {
		fmt.Fprintln(stderr, "[-] Error: at least one valid keyword from '-k' or '-kf' is required.")
		flags.Usage()
		return 2
	}
	if strings.TrimSpace(*output) == "" {
		*output = fmt.Sprintf("results_%d.json", deps.now().Unix())
	}
	d, err := detector.New()
	if err != nil {
		fmt.Fprintf(stderr, "[-] Error: initialize detector: %v\n", err)
		return 1
	}
	filter := results.Filter{Strict: *strict}
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "include" {
			filter.Include = include
		}
		if f.Name == "exclude" {
			filter.Exclude = exclude
		}
	})
	client := deps.newClient(stderr, *debug)
	all := make(map[string]results.Request)
	code, progress := 0, false
	color := supportsColor(stdout) && !*raw
	for _, keyword := range keywords {
		fmt.Fprintf(stderr, "[*] Searching for leaks related to keyword %s\n", keyword)
		found, searchErr := client.Search(ctx, keyword, postman.Options{
			ExtendWorkspaces: *extend,
			Filter:           filter,
			OnResult: func(request results.Request) error {
				all[request.ID()] = request
				if err := display(stdout, request, *raw, color); err != nil {
					return fmt.Errorf("write terminal output: %w", err)
				}
				return nil
			},
		})
		for _, request := range found {
			all[request.ID()] = request
		}
		if searchErr == nil || len(found) > 0 {
			progress = true
		}
		if searchErr != nil {
			fmt.Fprintf(stderr, "[-] Error: search keyword %q: %v\n", keyword, searchErr)
			code = 1
		}
		if code != 0 {
			break
		}
		fmt.Fprintf(stderr, "\n[*] %d results found for keyword '%s'. Happy (ethical) hacking!\n", len(found), keyword)
	}
	if !progress && code != 0 {
		return code
	}
	requests := results.Sorted(all)
	if err := results.Store(requests, *output); err != nil {
		fmt.Fprintf(stderr, "[-] Error: save results: %v\n", err)
		return 1
	}
	findings, err := d.ScanFile(*output)
	if err != nil {
		fmt.Fprintf(stderr, "[-] Error: detect secrets: %v\n", err)
		code = 1
	} else {
		for _, finding := range findings {
			fmt.Fprintf(stderr, " > Potential secret found: %s = %s\n", finding.Key, finding.Value)
		}
	}
	fmt.Fprintf(stderr, "[*] Saved %d unique results to %s\n", len(requests), *output)
	return code
}

func loadKeywords(keyword, path string) ([]string, error) {
	var candidates []string
	if strings.TrimSpace(keyword) != "" {
		candidates = append(candidates, strings.TrimSpace(keyword))
	}
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read keyword file: %w", err)
		}
		candidates = append(candidates, strings.Split(string(data), "\n")...)
	}
	keywords := make([]string, 0, len(candidates))
	seen := make(map[string]bool)
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" && !seen[candidate] {
			seen[candidate] = true
			keywords = append(keywords, candidate)
		}
	}
	return keywords, nil
}

func supportsColor(writer io.Writer) bool {
	if _, exists := os.LookupEnv("NO_COLOR"); exists {
		return false
	}
	f, ok := writer.(*os.File)
	if !ok || os.Getenv("TERM") == "dumb" {
		return false
	}
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func display(writer io.Writer, request results.Request, raw, color bool) error {
	if raw {
		encoder := json.NewEncoder(writer)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(request)
	}
	var text strings.Builder
	green, yellow, bold, reset := "", "", "", ""
	if color {
		green, yellow, bold, reset = "\033[92m", "\033[33m", "\033[1m", "\033[0m"
	}
	url := readable(request["url"])
	fmt.Fprintf(&text, "%s[+] (ID:%s) %s: %s%s%s%s", green, request.ID(), request.String("method"), bold, url, reset, yellow)
	if auth, exists := request["auth"]; exists && string(auth) != "null" {
		fmt.Fprintf(&text, "\n - Authentication items: %s", readable(auth))
	}
	for _, field := range []struct{ key, label string }{
		{"headerData", "Headers"}, {"data", "Misc. data items"}, {"queryParams", "Query parameters"},
	} {
		items := dataItems(request[field.key])
		if len(items) == 0 {
			continue
		}
		fmt.Fprintf(&text, "\n - %s: ", field.label)
		for _, item := range items {
			fmt.Fprintf(&text, "[%s=%s]", readable(item["key"]), readable(item["value"]))
		}
	}
	fmt.Fprintf(&text, "%s\n", reset)
	_, err := io.WriteString(writer, text.String())
	return err
}

func readable(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil && string(raw) != "null" {
		return strconv.Quote(text)
	}
	if len(raw) == 0 {
		return "null"
	}
	return string(raw)
}

func dataItems(raw json.RawMessage) []map[string]json.RawMessage {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		// Some Postman requests store the data array as a JSON-encoded string.
		var encoded string
		if json.Unmarshal(raw, &encoded) != nil || json.Unmarshal([]byte(encoded), &items) != nil {
			return nil
		}
	}
	var found []map[string]json.RawMessage
	for _, rawItem := range items {
		var item map[string]json.RawMessage
		if json.Unmarshal(rawItem, &item) == nil && item != nil {
			if _, exists := item["key"]; exists {
				found = append(found, item)
			}
			continue
		}
		var encoded string
		if json.Unmarshal(rawItem, &encoded) == nil && strings.HasPrefix(encoded, "[") {
			found = append(found, dataItems(json.RawMessage(encoded))...)
		}
	}
	return found
}
