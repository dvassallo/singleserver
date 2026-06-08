package singleserver

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var envKeyPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func appEnvPath(appName string) string {
	return filepath.Join(envDefault("SINGLESERVER_STATE_DIR", "/etc/singleserver"), "env", appName+".env")
}

func loadAppEnv(appName string) (map[string]string, error) {
	path := appEnvPath(appName)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	defer file.Close()

	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s contains an invalid env line: %q", path, line)
		}
		key = strings.TrimSpace(key)
		if !envKeyPattern.MatchString(key) {
			return nil, fmt.Errorf("%s contains an invalid env key: %q", path, key)
		}
		values[key] = unquoteEnvValue(strings.TrimSpace(value))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func writeAppEnv(appName string, values map[string]string) error {
	keys := sortedEnvKeys(values)
	var builder strings.Builder
	for _, key := range keys {
		if !envKeyPattern.MatchString(key) {
			return fmt.Errorf("invalid env key: %q", key)
		}
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(shellQuote(values[key]))
		builder.WriteByte('\n')
	}
	return writeFileAtomic(appEnvPath(appName), []byte(builder.String()))
}

func sortedEnvKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func unquoteEnvValue(value string) string {
	if len(value) >= 2 && strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") {
		value = strings.TrimPrefix(strings.TrimSuffix(value, "'"), "'")
		return strings.ReplaceAll(value, "'\"'\"'", "'")
	}
	if len(value) >= 2 && strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"") {
		value = strings.TrimPrefix(strings.TrimSuffix(value, "\""), "\"")
		value = strings.ReplaceAll(value, `\"`, `"`)
		value = strings.ReplaceAll(value, `\\`, `\`)
	}
	return value
}

func parseKeyValue(arg string) (string, string, error) {
	key, value, ok := strings.Cut(arg, "=")
	if !ok {
		return "", "", errors.New("expected KEY=value")
	}
	key = strings.TrimSpace(key)
	if !envKeyPattern.MatchString(key) {
		return "", "", fmt.Errorf("invalid env key: %q", key)
	}
	return key, value, nil
}

func appSecretKeys(appName string) ([]string, error) {
	values, err := loadAppEnv(appName)
	if err != nil {
		return nil, err
	}
	return sortedEnvKeys(values), nil
}
