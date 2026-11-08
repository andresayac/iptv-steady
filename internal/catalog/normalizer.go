package catalog

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"iptv-steady/internal/config"
)

var (
	parenGroup = regexp.MustCompile(`\(([^)]*)\)`)
	leadingTag = regexp.MustCompile(`^\s*([\p{L}\p{N}]+)\s*[-|:]\s*`)
	yearValue  = regexp.MustCompile(`\b(\d{4})\b`)
)

const edgePunctuation = " -|.:"

func fold(s string) string {
	chain := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	folded, _, err := transform.String(chain, strings.ToLower(s))
	if err != nil {
		return strings.ToLower(s)
	}
	return folded
}

func tokenize(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func compilePatterns(patterns []string) ([]*regexp.Regexp, error) {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		re, err := regexp.Compile("(?i)" + pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid pattern %q: %w", pattern, err)
		}
		compiled = append(compiled, re)
	}
	return compiled, nil
}

func wordSet(words []string) map[string]bool {
	set := make(map[string]bool, len(words))
	for _, word := range words {
		set[fold(word)] = true
	}
	return set
}

func matchesAny(patterns []*regexp.Regexp, text string) bool {
	for _, re := range patterns {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

func titleCase(s string, uppercase map[string]bool) string {
	words := strings.Fields(s)
	for i, word := range words {
		if uppercase[fold(word)] {
			words[i] = strings.ToUpper(word)
			continue
		}
		letters := []rune(strings.ToLower(word))
		letters[0] = unicode.ToUpper(letters[0])
		words[i] = string(letters)
	}
	return strings.Join(words, " ")
}

func trimYearSuffix(name string, year int) string {
	if year == 0 {
		return name
	}
	return strings.TrimSuffix(name, fmt.Sprintf(" (%d)", year))
}

// normalizer turns provider names into comparison keys and display names using rules from the configuration.
type normalizer struct {
	years     []*regexp.Regexp
	fallback  *regexp.Regexp
	remove    []*regexp.Regexp
	poster    *regexp.Regexp
	tags      map[string]bool
	trailing  map[string]bool
	stop      map[string]bool
	uppercase map[string]bool
	minYear   int
	maxYear   int
}

func newNormalizer(cfg config.Identity, uppercase []string) (*normalizer, error) {
	years, err := compilePatterns(cfg.YearPatterns)
	if err != nil {
		return nil, err
	}
	remove, err := compilePatterns(cfg.RemovePatterns)
	if err != nil {
		return nil, err
	}
	singles, err := compilePatterns([]string{cfg.FallbackYearPattern, cfg.PosterPattern})
	if err != nil {
		return nil, err
	}
	return &normalizer{
		years: years, remove: remove, fallback: singles[0], poster: singles[1],
		tags: wordSet(cfg.TagWords), trailing: wordSet(cfg.TrailingWords), stop: wordSet(cfg.StopWords), uppercase: wordSet(uppercase),
		minYear: cfg.MinYear, maxYear: time.Now().Year() + cfg.MaxYearAhead,
	}, nil
}

func (n *normalizer) validYear(value string) int {
	year, _ := strconv.Atoi(yearValue.FindString(value))
	if year < n.minYear || year > n.maxYear {
		return 0
	}
	return year
}

func (n *normalizer) yearIn(text string) int {
	for _, pattern := range n.years {
		matches := pattern.FindAllStringSubmatch(text, -1)
		for i := len(matches) - 1; i >= 0; i-- {
			if year := n.validYear(matches[i][1]); year != 0 {
				return year
			}
		}
	}
	return 0
}

func (n *normalizer) withoutYears(text string) string {
	for _, pattern := range n.years {
		text = pattern.ReplaceAllString(text, " ")
	}
	return text
}

func (n *normalizer) onlyTags(text string) bool {
	for _, token := range tokenize(fold(text)) {
		if !n.tags[token] {
			return false
		}
	}
	return true
}

func (n *normalizer) withoutTags(text string, keepParentheses bool) string {
	for _, pattern := range n.remove {
		text = pattern.ReplaceAllString(text, " ")
	}
	text = parenGroup.ReplaceAllStringFunc(text, func(group string) string {
		inner := group[1 : len(group)-1]
		switch {
		case n.onlyTags(inner):
			return " "
		case keepParentheses:
			return group
		}
		return " " + inner + " "
	})
	for {
		match := leadingTag.FindStringSubmatch(text)
		if match == nil || !n.tags[fold(match[1])] {
			return text
		}
		text = text[len(match[0]):]
	}
}

// parse returns the comparison key of a title and its year, 0 when unknown.
func (n *normalizer) parse(name, yearField, release string) (string, int) {
	text := fold(name)
	year := n.yearIn(text)
	if year == 0 {
		year = n.validYear(yearField)
	}
	if year == 0 {
		year = n.validYear(release)
	}
	tokens := tokenize(n.withoutTags(n.withoutYears(text), false))
	for len(tokens) > 1 && n.trailing[tokens[len(tokens)-1]] {
		tokens = tokens[:len(tokens)-1]
	}
	if year == 0 && len(tokens) > 1 {
		if match := n.fallback.FindStringSubmatch(strings.Join(tokens, " ")); match != nil {
			if year = n.validYear(match[1]); year != 0 {
				tokens = tokens[:len(tokens)-1]
			}
		}
	}
	kept := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if !n.stop[token] {
			kept = append(kept, token)
		}
	}
	if len(kept) == 0 {
		kept = tokens
	}
	return strings.Join(kept, " "), year
}

// display removes year, tags and uploader blocks from a provider's name; the caller adds the year back.
func (n *normalizer) display(name string, year int) string {
	text := n.withoutTags(n.withoutYears(name), true)
	words := strings.Fields(strings.ReplaceAll(text, "_", " "))
	for len(words) > 1 && n.trailing[fold(strings.Trim(words[len(words)-1], edgePunctuation))] {
		words = words[:len(words)-1]
	}
	text = strings.Trim(strings.Join(words, " "), edgePunctuation)
	if year > 0 {
		text = strings.TrimSuffix(text, " "+strconv.Itoa(year))
	}
	if text == "" {
		return strings.TrimSpace(name)
	}
	return n.tidyCase(text)
}

func (n *normalizer) tidyCase(s string) string {
	hasLower, hasUpper := false, false
	for _, r := range s {
		hasLower = hasLower || unicode.IsLower(r)
		hasUpper = hasUpper || unicode.IsUpper(r)
	}
	if hasLower && hasUpper {
		return s
	}
	return titleCase(s, n.uppercase)
}

func (n *normalizer) posterKey(url string) string {
	if match := n.poster.FindStringSubmatch(url); match != nil {
		return match[1]
	}
	return ""
}
