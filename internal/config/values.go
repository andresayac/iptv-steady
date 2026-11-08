package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

type ByteSize int64

var (
	byteSizePattern = regexp.MustCompile(`^(\d+)\s*([A-Za-z]*)$`)
	byteUnits       = map[string]int64{"": 1, "B": 1, "KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30}
	envReference    = regexp.MustCompile(`\$\{(\w+)\}`)
)

func (b *ByteSize) UnmarshalYAML(node *yaml.Node) error {
	match := byteSizePattern.FindStringSubmatch(strings.TrimSpace(node.Value))
	if match == nil {
		return fmt.Errorf("line %d: invalid size %q", node.Line, node.Value)
	}
	unit, known := byteUnits[match[2]]
	if !known {
		return fmt.Errorf("line %d: unknown size unit %q", node.Line, match[2])
	}
	value, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return fmt.Errorf("line %d: %w", node.Line, err)
	}
	*b = ByteSize(value * unit)
	return nil
}

func loadEnvFile(path string) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, found := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if _, exists := os.LookupEnv(name); !found || exists {
			continue
		}
		if err := os.Setenv(name, strings.Trim(strings.TrimSpace(value), `"'`)); err != nil {
			return err
		}
	}
	return scanner.Err()
}

type expander struct {
	missing []string
}

func (e *expander) apply(value *string) {
	*value = envReference.ReplaceAllStringFunc(*value, func(match string) string {
		name := match[2 : len(match)-1]
		resolved, ok := os.LookupEnv(name)
		if !ok && !slices.Contains(e.missing, name) {
			e.missing = append(e.missing, name)
		}
		return resolved
	})
}

func (e *expander) err() error {
	if len(e.missing) == 0 {
		return nil
	}
	return fmt.Errorf("missing environment variables: %s", strings.Join(e.missing, ", "))
}
