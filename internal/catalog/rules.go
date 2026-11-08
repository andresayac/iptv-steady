package catalog

import (
	"regexp"
	"sort"
	"strings"
	"unicode"

	"iptv-steady/internal/config"
)

type filterSet struct {
	content   []*regexp.Regexp
	allow     []*regexp.Regexp
	category  []*regexp.Regexp
	names     []*regexp.Regexp
	adultFlag bool
}

func newFilterSet(cfg config.Filters) (*filterSet, error) {
	sets := [][]string{cfg.BlockContent, cfg.AllowCategories, cfg.BlockCategories, cfg.BlockNames}
	compiled := make([][]*regexp.Regexp, len(sets))
	for i, patterns := range sets {
		var err error
		if compiled[i], err = compilePatterns(patterns); err != nil {
			return nil, err
		}
	}
	return &filterSet{content: compiled[0], allow: compiled[1], category: compiled[2], names: compiled[3], adultFlag: cfg.HonorAdultFlag}, nil
}

func (f *filterSet) blocksContent(foldedCategory, foldedName string, adult bool) bool {
	if f.adultFlag && adult {
		return true
	}
	if matchesAny(f.names, foldedName) {
		return true
	}
	return matchesAny(f.content, foldedCategory) && !matchesAny(f.allow, foldedCategory)
}

func (f *filterSet) hidesCategory(foldedCategory string) bool {
	return matchesAny(f.category, foldedCategory)
}

type trait struct {
	pattern *regexp.Regexp
	score   int
	label   string
}

func newTraits(cfg []config.CopyTrait) ([]trait, error) {
	traits := make([]trait, 0, len(cfg))
	for _, item := range cfg {
		compiled, err := compilePatterns([]string{item.Pattern})
		if err != nil {
			return nil, err
		}
		traits = append(traits, trait{compiled[0], item.Score, item.Label})
	}
	return traits, nil
}

// evaluateTraits returns a copy's score and the label of its most penalized trait.
func evaluateTraits(traits []trait, text string) (int, string) {
	score, label, worst := 0, "", 0
	for _, t := range traits {
		if !t.pattern.MatchString(text) {
			continue
		}
		score += t.score
		if t.label != "" && t.score < worst {
			label, worst = t.label, t.score
		}
	}
	return score, label
}

// categorizer gives equivalent provider categories the same identity.
type categorizer struct {
	fallback   string
	noise      map[string]bool
	dropNumber bool
	uppercase  map[string]bool
	aliases    map[string]string
	pinned     []*regexp.Regexp
}

func newCategorizer(cfg config.Categories) (*categorizer, error) {
	pinned, err := compilePatterns(cfg.Pinned)
	if err != nil {
		return nil, err
	}
	return &categorizer{fallback: cfg.Fallback, noise: wordSet(cfg.NoiseWords), dropNumber: cfg.DropTrailingNumber, uppercase: wordSet(cfg.UppercaseWords), aliases: cfg.Aliases, pinned: pinned}, nil
}

func (c *categorizer) meaningful(name string) []string {
	tokens := tokenize(name)
	kept := make([]string, 0, len(tokens))
	for _, token := range tokens {
		if !c.noise[fold(token)] {
			kept = append(kept, token)
		}
	}
	if len(kept) == 0 {
		kept = tokens
	}
	if c.dropNumber && len(kept) > 1 {
		if last := []rune(kept[len(kept)-1]); len(last) == 1 && unicode.IsDigit(last[0]) {
			kept = kept[:len(kept)-1]
		}
	}
	return kept
}

func (c *categorizer) key(name string) string {
	return strings.Join(c.meaningful(fold(name)), " ")
}

func (c *categorizer) orFallback(name string) string {
	if strings.TrimSpace(name) == "" {
		return c.fallback
	}
	return name
}

// origin is the provider's own category key, which stays the same however categories are renamed or joined.
func (c *categorizer) origin(name string) string {
	return c.key(c.orFallback(name))
}

func (c *categorizer) identify(name string) (string, string) {
	name = c.orFallback(name)
	key := c.key(name)
	if shown, found := c.aliases[key]; found {
		return c.key(shown), shown
	}
	return key, titleCase(strings.Join(c.meaningful(name), " "), c.uppercase)
}

func (c *categorizer) pinRank(key string) int {
	for i, re := range c.pinned {
		if re.MatchString(key) {
			return i
		}
	}
	return len(c.pinned)
}

// sortKeys places pinned categories first, newest first within a pin, then the rest alphabetically.
func (c *categorizer) sortKeys(keys []string) {
	sort.Slice(keys, func(i, j int) bool {
		rankI, rankJ := c.pinRank(keys[i]), c.pinRank(keys[j])
		if rankI != rankJ {
			return rankI < rankJ
		}
		if rankI < len(c.pinned) {
			return keys[i] > keys[j]
		}
		return keys[i] < keys[j]
	})
}

func hasAccents(s string) bool {
	return len([]rune(s)) < len(s)
}

// preferredSpelling keeps the variant with accents when providers differ.
func preferredSpelling(current, candidate string) string {
	if current == "" || (hasAccents(candidate) && !hasAccents(current)) {
		return candidate
	}
	return current
}
