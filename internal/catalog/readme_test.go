package catalog

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"iptv-steady/internal/config"
)

// The configuration recipes in the README must keep loading as the configuration evolves.
func TestReadmeRecipesLoad(t *testing.T) {
	for name, value := range map[string]string{
		"LISTEN": ":1", "PUBLIC_URL": "", "PROXY_USER": "viewer", "PROXY_PASSWORD": "secret",
		"PROVIDER_ONE_URL": "https://one.example", "PROVIDER_ONE_USER": "user", "PROVIDER_ONE_PASSWORD": "secret",
		"PROVIDER_TWO_URL": "https://two.example", "PROVIDER_TWO_USER": "user", "PROVIDER_TWO_PASSWORD": "secret",
	} {
		t.Setenv(name, value)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, section, found := strings.Cut(string(raw), "### Adapt it")
	if !found {
		t.Fatal("the README has no recipes section")
	}
	section, _, _ = strings.Cut(section, "\n## ")
	recipes := regexp.MustCompile("(?s)```yaml\n(.*?)```").FindAllStringSubmatch(section, -1)
	if len(recipes) == 0 {
		t.Fatal("the README has no recipes")
	}
	for _, recipe := range recipes {
		cfg, err := config.Load(filepath.Join("..", "..", "config.example.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		decoder := yaml.NewDecoder(strings.NewReader(recipe[1]))
		decoder.KnownFields(true)
		if err := decoder.Decode(cfg); err != nil {
			t.Fatalf("recipe does not load: %v\n%s", err, recipe[1])
		}
		if !strings.HasPrefix(recipe[1], "accounts:") {
			if err := cfg.Validate(); err != nil {
				t.Fatalf("recipe is invalid: %v\n%s", err, recipe[1])
			}
		}
		if _, err := NewBuilder(cfg.Catalog, &IDs{}); err != nil {
			t.Fatalf("recipe has a bad pattern: %v\n%s", err, recipe[1])
		}
	}
}
