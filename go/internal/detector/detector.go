// Package detector ports the JSON detection path of Whispers 2.2.1.
// See NOTICE and WHISPERS_LICENSE for attribution and license terms.
package detector

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dlclark/regexp2"
	"gopkg.in/yaml.v3"
)

//go:embed config.yml rules/*.yml NOTICE WHISPERS_LICENSE
var assets embed.FS

type specification struct {
	Regex      string `yaml:"regex"`
	IgnoreCase bool   `yaml:"ignorecase"`
	MinLen     int    `yaml:"minlen"`
	IsBase64   *bool  `yaml:"isBase64"`
	IsASCII    *bool  `yaml:"isAscii"`
	IsURI      *bool  `yaml:"isUri"`
	IsLuhn     *bool  `yaml:"isLuhn"`
	IsFile     bool   `yaml:"isFile"`
	compiled   *regexp2.Regexp
}

type rule struct {
	ID         string         `yaml:"id"`
	Group      string         `yaml:"group"`
	Message    string         `yaml:"message"`
	Severity   string         `yaml:"severity"`
	Key        *specification `yaml:"key"`
	Value      *specification `yaml:"value"`
	Similarity *float64       `yaml:"similar"`
}

type Detector struct {
	rules []rule
}

type Finding struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type pair struct {
	key   string
	value string
	file  bool
}

func New() (*Detector, error) {
	files, err := assets.ReadDir("rules")
	if err != nil {
		return nil, err
	}
	available := make(map[string]rule)
	for _, file := range files {
		data, err := assets.ReadFile("rules/" + file.Name())
		if err != nil {
			return nil, err
		}
		var rules []rule
		if err := yaml.Unmarshal(data, &rules); err != nil {
			return nil, fmt.Errorf("load rules %s: %w", file.Name(), err)
		}
		for _, r := range rules {
			available[r.ID] = r
		}
	}
	data, err := assets.ReadFile("config.yml")
	if err != nil {
		return nil, err
	}
	var config struct {
		Include struct {
			Rules []any `yaml:"rules"`
		} `yaml:"include"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	d := &Detector{}
	for _, item := range config.Include.Rules {
		var r rule
		if id, ok := item.(string); ok {
			var exists bool
			r, exists = available[id]
			if !exists {
				return nil, fmt.Errorf("unknown rule %q", id)
			}
		} else {
			data, err := yaml.Marshal(item)
			if err != nil {
				return nil, err
			}
			if err := yaml.Unmarshal(data, &r); err != nil {
				return nil, err
			}
		}
		if r.ID == "" || r.Group == "" || r.Message == "" || r.Severity == "" {
			return nil, fmt.Errorf("invalid rule %q", r.ID)
		}
		for _, spec := range []*specification{r.Key, r.Value} {
			if spec == nil || spec.Regex == "" {
				continue
			}
			options := regexp2.None
			if spec.IgnoreCase {
				options |= regexp2.IgnoreCase
			}
			// Python re.match always starts at index zero, even for unanchored patterns.
			spec.compiled, err = regexp2.Compile("\\A(?:"+spec.Regex+")", options)
			if err != nil {
				return nil, fmt.Errorf("compile rule %s: %w", r.ID, err)
			}
			spec.compiled.MatchTimeout = time.Second
		}
		d.rules = append(d.rules, r)
	}
	return d, nil
}

func (d *Detector) ScanFile(path string) ([]Finding, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return d.Scan(f, path)
}

// Scan reads the exported JSON document and returns unique key/value findings.
func (d *Detector) Scan(reader io.Reader, path string) ([]Finding, error) {
	decoder := json.NewDecoder(reader)
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("parse results for detection: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("unexpected data after JSON document")
	}
	pairs := []pair{{key: "file", value: path, file: true}}
	traverse(document, "", true, &pairs)
	unique := make(map[Finding]bool)
	for _, p := range pairs {
		if !p.file {
			p.key = strings.Trim(p.key, " '\"\n\r\t")
			p.value = strings.Trim(p.value, " '\"\n\r\t")
			if !isStatic(p.key, p.value) {
				continue
			}
		}
		for _, r := range d.rules {
			ok, err := r.matches(p)
			if err != nil {
				return nil, fmt.Errorf("match rule %s: %w", r.ID, err)
			}
			if ok {
				unique[Finding{Key: p.key, Value: p.value}] = true
			}
		}
	}
	findings := make([]Finding, 0, len(unique))
	for f := range unique {
		findings = append(findings, f)
	}
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Key == findings[j].Key {
			return findings[i].Value < findings[j].Value
		}
		return findings[i].Key < findings[j].Key
	})
	return findings, nil
}

func (r rule) matches(p pair) (bool, error) {
	if p.key == "file" && (r.Value == nil || !r.Value.IsFile) {
		return false, nil
	}
	for _, target := range []struct {
		spec  *specification
		value string
	}{{r.Key, p.key}, {r.Value, p.value}} {
		if target.spec == nil {
			continue
		}
		ok, err := target.spec.matches(target.value)
		if !ok || err != nil {
			return false, err
		}
	}
	threshold := 1.0
	if r.Similarity != nil {
		threshold = *r.Similarity
	}
	return similarity(p.key, p.value) < threshold, nil
}

func (s specification) matches(value string) (bool, error) {
	if utf8.RuneCountInString(value) < s.MinLen {
		return false, nil
	}
	if s.compiled != nil {
		ok, err := s.compiled.MatchString(value)
		if !ok || err != nil {
			return false, err
		}
	}
	decoded := []byte(value)
	if s.IsBase64 != nil && *s.IsBase64 {
		var err error
		decoded, err = decodeBase64(value)
		if err != nil {
			return false, nil
		}
		if s.IsASCII == nil || *s.IsASCII {
			if !utf8.Valid(decoded) {
				return false, nil
			}
		} else if len(decoded) == 0 {
			return false, nil
		}
	}
	if s.IsASCII != nil && *s.IsASCII != isASCII(string(decoded)) {
		return false, nil
	}
	if s.IsURI != nil && *s.IsURI != isURI(string(decoded)) {
		return false, nil
	}
	if s.IsLuhn != nil && *s.IsLuhn != isLuhn(string(decoded)) {
		return false, nil
	}
	return true, nil
}

func scalar(value any) (string, bool) {
	switch value := value.(type) {
	case string:
		return value, true
	case json.Number:
		// Whispers treats integer leaves as pairs, but not floating-point leaves.
		if !strings.ContainsAny(string(value), ".eE") {
			return string(value), true
		}
	case bool:
		if value {
			return "True", true
		}
		return "", true // Python strip_string(False) returns an empty string.
	}
	return "", false
}

func traverse(value any, key string, root bool, pairs *[]pair) {
	switch value := value.(type) {
	case map[string]any:
		if root {
			if _, ok := value["AWSTemplateFormatVersion"]; ok {
				if parameters, ok := value["Parameters"].(map[string]any); ok {
					for name, parameter := range parameters {
						if values, ok := parameter.(map[string]any); ok {
							if defaultValue, ok := scalar(values["Default"]); ok {
								*pairs = append(*pairs, pair{key: name, value: defaultValue})
							}
						}
					}
				}
			}
		}
		keys := make([]string, 0, len(value))
		for k := range value {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := value[k]
			if text, ok := scalar(v); ok {
				*pairs = append(*pairs, pair{key: k, value: text})
			}
			traverse(v, k, false, pairs)
		}
		if k, ok := scalar(value["key"]); ok {
			if v, ok := scalar(value["value"]); ok {
				*pairs = append(*pairs, pair{key: k, value: v})
			}
		}
	case []any:
		for _, item := range value {
			if text, ok := scalar(item); ok {
				*pairs = append(*pairs, pair{key: key, value: text})
			}
			traverse(item, key, false, pairs)
		}
	case string:
		*pairs = append(*pairs, stringPairs(value)...)
	}
}

// License returns the embedded third-party notices for binary distributions.
func License() string {
	notice, _ := assets.ReadFile("NOTICE")
	license, _ := assets.ReadFile("WHISPERS_LICENSE")
	return string(bytes.Join([][]byte{notice, license}, []byte("\n")))
}
