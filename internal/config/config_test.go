package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const minimal = `
server: {listen: ":1"}
accounts:
  - {name: one, url: "${URL_VALUE}", username: "${USER_VALUE}", password: secret}
`

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMissingEnvironmentVariableIsNamed(t *testing.T) {
	_, err := Load(writeFile(t, "config.yaml", minimal))
	if err == nil || !strings.Contains(err.Error(), "URL_VALUE") {
		t.Fatalf("error = %v", err)
	}
}

func TestUnknownFieldsAreRejected(t *testing.T) {
	_, err := Load(writeFile(t, "config.yaml", "servr: {listen: x}"))
	if err == nil || !strings.Contains(err.Error(), "servr") {
		t.Fatalf("error = %v", err)
	}
}

func TestIncompleteConfigurationListsWhatIsMissing(t *testing.T) {
	t.Setenv("URL_VALUE", "https://provider.example/")
	t.Setenv("USER_VALUE", "user")
	_, err := Load(writeFile(t, "config.yaml", minimal))
	for _, want := range []string{"file.stall_timeout must be positive", "at least one user is required", "data_dir is required"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("error should mention %q: %v", want, err)
		}
	}
}

func TestUnknownCategorySectionIsRejected(t *testing.T) {
	cfg := &Config{Catalog: Catalog{Categories: Categories{Sections: map[string]CategorySection{"movies": {}}}}}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), `unknown section "movies"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestReserveMarksMustBeOrdered(t *testing.T) {
	cfg := &Config{File: File{Reserve: Reserve{Empty: 30, Low: 3, Full: 60, MinSupply: 1.5}}}
	err := cfg.Validate()
	for _, want := range []string{"file.reserve needs empty < low < full", "file.reserve.min_supply must be above 0 and at most 1"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("error should mention %q: %v", want, err)
		}
	}
}

func TestEnvFileNeverOverridesTheEnvironment(t *testing.T) {
	t.Setenv("KEPT_VALUE", "from-environment")
	path := writeFile(t, ".env", "# comment\nKEPT_VALUE=from-file\nNEW_VALUE=\"quoted\"\n")
	t.Cleanup(func() { os.Unsetenv("NEW_VALUE") })
	if err := loadEnvFile(path); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("KEPT_VALUE") != "from-environment" || os.Getenv("NEW_VALUE") != "quoted" {
		t.Fatalf("got %q and %q", os.Getenv("KEPT_VALUE"), os.Getenv("NEW_VALUE"))
	}
}

func TestByteSizeUnits(t *testing.T) {
	path := writeFile(t, "config.yaml", "file: {read_ahead: 64MiB, chunk_size: 2KiB, tail_check: 10}")
	cfg := &Config{}
	raw, _ := os.ReadFile(path)
	if err := decodeInto(raw, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.File.ReadAhead != 64<<20 || cfg.File.ChunkSize != 2<<10 || cfg.File.TailCheck != 10 {
		t.Fatalf("sizes = %+v", cfg.File)
	}
	if err := decodeInto([]byte("file: {read_ahead: 5TB}"), &Config{}); err == nil {
		t.Fatal("an unknown unit should be rejected")
	}
}

func TestExampleConfigurationIsValid(t *testing.T) {
	for name, value := range map[string]string{
		"LISTEN": ":1", "PUBLIC_URL": "", "PROXY_USER": "viewer", "PROXY_PASSWORD": "secret",
		"PROVIDER_ONE_URL": "https://one.example", "PROVIDER_ONE_USER": "user", "PROVIDER_ONE_PASSWORD": "secret",
		"PROVIDER_TWO_URL": "https://two.example", "PROVIDER_TWO_USER": "user", "PROVIDER_TWO_PASSWORD": "secret",
	} {
		t.Setenv(name, value)
	}
	if _, err := Load(filepath.Join("..", "..", "config.example.yaml")); err != nil {
		t.Fatal(err)
	}
}
