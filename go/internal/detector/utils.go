package detector

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	envPattern  = regexp.MustCompile(`^\$\$?\{?[A-Z0-9_]+\}?$`)
	pathPattern = regexp.MustCompile(`(?i)^((([A-Z]|file|root):)?(\.+)?[/\\]+).*$`)
	iacPattern  = regexp.MustCompile(`^![A-Za-z]+ .+`)
	uriPattern  = regexp.MustCompile(`(?i)^[:\pL\pN_]+://.+`)
)

func isASCII(value string) bool {
	for _, ch := range value {
		if (ch < 32 || ch > 126) && !strings.ContainsRune("\t\n\r\v\f", ch) {
			return false
		}
	}
	return true
}

func isURI(value string) bool {
	return isASCII(value) && !strings.ContainsAny(value, " \t\n\r\v\f") && uriPattern.MatchString(value)
}

func decodeBase64(value string) ([]byte, error) {
	// Python b64decode ignores non-alphabet characters, accepts surplus padding,
	// and stops at a padded quartet. It does not supply missing padding.
	var filtered strings.Builder
	for _, ch := range value {
		if ch > 127 {
			return nil, fmt.Errorf("non-ASCII base64 input")
		}
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '+' || ch == '/' || ch == '=' {
			filtered.WriteRune(ch)
		}
	}
	text := filtered.String()
	var alphabet strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] != '=' {
			alphabet.WriteByte(text[i])
			continue
		}
		n := alphabet.Len() % 4
		if n == 3 {
			return base64.StdEncoding.DecodeString(alphabet.String() + "=")
		}
		if n == 2 && i+1 < len(text) && text[i+1] == '=' {
			return base64.StdEncoding.DecodeString(alphabet.String() + "==")
		}
	}
	return base64.StdEncoding.DecodeString(alphabet.String())
}

func simple(value string) string {
	value = strings.Trim(value, " '\"\n\r\t")
	value = strings.ToLower(strings.TrimRight(value, "\\"))
	var b strings.Builder
	for _, ch := range value {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			b.WriteRune(ch)
		}
	}
	return b.String()
}

// simplePlaceholder preserves the underscores used by Whispers is_static.
func simplePlaceholder(value string) string {
	value = strings.Trim(value, " '\"\n\r\t")
	value = strings.ToLower(strings.TrimRight(value, "\\"))
	var b strings.Builder
	for _, ch := range strings.TrimSpace(value) {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') {
			b.WriteRune(ch)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func isStatic(key, value string) bool {
	if value == "" || strings.EqualFold(value, "null") {
		return false
	}
	if strings.HasPrefix(value, "$") && envPattern.MatchString(value) {
		return false
	}
	if strings.HasPrefix(value, "%") && strings.HasSuffix(value, "%") {
		return false
	}
	if strings.HasPrefix(value, "${") && strings.HasSuffix(value, "}") {
		return false
	}
	if strings.HasPrefix(value, "{") && strings.HasSuffix(value, "}") {
		if len([]rune(value)) > 50 {
			if decoded, err := decodeBase64(value[1 : len(value)-1]); err == nil && len(decoded) > 0 {
				return true
			}
		}
		return false
	}
	if strings.Contains(value, "{{") && strings.Contains(value, "}}") {
		return false
	}
	if strings.HasPrefix(value, "<") && strings.HasSuffix(value, ">") {
		return false
	}
	if strings.HasPrefix(value, "ENC[AES256_GCM,data:") && strings.HasSuffix(value, "]") {
		return false
	}
	k, v := simplePlaceholder(key), simplePlaceholder(value)
	if k == v || strings.HasSuffix(v, k) {
		return false
	}
	return !(isASCII(value) && (iacPattern.MatchString(value) || pathPattern.MatchString(value)))
}

func isLuhn(value string) bool {
	if value == "" || !isASCII(value) {
		return false
	}
	sum := 0
	double := false
	for i := len(value) - 1; i >= 0; i-- {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
		digit := int(value[i] - '0')
		if double {
			digit *= 2
			if digit > 9 {
				digit -= 9
			}
		}
		sum += digit
		double = !double
	}
	return sum%10 == 0
}

func stringPairs(text string) []pair {
	var pairs []pair
	key, value := "", text
	if strings.Count(value, "=") == 1 {
		key, value, _ = strings.Cut(value, "=")
	}
	if strings.Contains(value, ":-") && strings.HasPrefix(value, "${") && strings.HasSuffix(value, "}") {
		key, value, _ = strings.Cut(value[2:len(value)-1], ":-")
	}
	if key != "" || value != "" {
		pairs = append(pairs, pair{key: key, value: value})
	}
	for _, word := range strings.Split(text, " ") {
		if strings.Contains(word, "://") {
			uri, err := url.Parse(word)
			if err == nil {
				if uri.User != nil {
					if password, ok := uri.User.Password(); ok && password != "" {
						// urllib.urlparse leaves percent escapes in userinfo intact.
						userinfo := uri.User.String()
						pairs = append(pairs, pair{key: "uri_creds", value: userinfo})
					}
				}
				for _, part := range strings.Split(uri.RawQuery, "&") {
					k, v, _ := strings.Cut(part, "=")
					k, keyErr := url.QueryUnescape(k)
					v, valueErr := url.QueryUnescape(v)
					if keyErr == nil && valueErr == nil && v != "" {
						pairs = append(pairs, pair{key: k, value: v})
					}
				}
			}
		}
		if strings.HasPrefix(word, "arn:aws:") {
			parts := strings.Split(word, ":")
			if len(parts) >= 5 && parts[4] != "" {
				pairs = append(pairs, pair{key: "aws_account", value: parts[4]})
			}
		}
	}
	return pairs
}

// similarity implements the Jaro-Winkler score used by jellyfish.
func similarity(first, second string) float64 {
	a, b := []rune(simple(first)), []rune(simple(second))
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	if string(a) == string(b) {
		return 1
	}
	window := max(len(a), len(b))/2 - 1
	window = max(window, 0)
	matchedA, matchedB := make([]bool, len(a)), make([]bool, len(b))
	matches := 0
	for i, ch := range a {
		for j := max(0, i-window); j < min(len(b), i+window+1); j++ {
			if !matchedB[j] && ch == b[j] {
				matchedA[i], matchedB[j] = true, true
				matches++
				break
			}
		}
	}
	if matches == 0 {
		return 0
	}
	transpositions, j := 0, 0
	for i := range a {
		if !matchedA[i] {
			continue
		}
		for !matchedB[j] {
			j++
		}
		if a[i] != b[j] {
			transpositions++
		}
		j++
	}
	m := float64(matches)
	// jellyfish counts half-transpositions using integer division.
	score := (m/float64(len(a)) + m/float64(len(b)) + (m-float64(transpositions/2))/m) / 3
	if score > 0.7 {
		prefix := 0
		for i := 0; i < min(4, min(len(a), len(b))) && a[i] == b[i]; i++ {
			prefix++
		}
		score += float64(prefix) * 0.1 * (1 - score)
	}
	return score
}
