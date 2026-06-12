package singleserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteAndLoadAppEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SINGLESERVER_STATE_DIR", dir)

	values := map[string]string{
		"PLAIN":  "hello",
		"SPACED": "hello world",
		"QUOTE":  "it's fine",
	}
	if err := writeAppEnv("my-app", values); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(filepath.Join(dir, "env", "my-app.env"))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "PLAIN='hello'\nQUOTE='it'\"'\"'s fine'\nSPACED='hello world'\n" {
		t.Fatalf("unexpected env body:\n%s", body)
	}

	loaded, err := loadAppEnv("my-app")
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range values {
		if loaded[key] != want {
			t.Fatalf("%s: got %q, want %q", key, loaded[key], want)
		}
	}
}

func TestParseKeyValueRejectsBadKey(t *testing.T) {
	if _, _, err := parseKeyValue("1BAD=value"); err == nil {
		t.Fatal("expected bad key to fail")
	}
}

func TestUnquoteEnvValue(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{value: `'it'"'"'s fine'`, want: "it's fine"},
		{value: `"quoted \"value\""`, want: `quoted "value"`},
		{value: `"path\\to\\file"`, want: `path\to\file`},
		{value: "plain", want: "plain"},
		{value: "''", want: ""},
	}

	for _, test := range tests {
		if got := unquoteEnvValue(test.value); got != test.want {
			t.Fatalf("unquoteEnvValue(%q) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestLoadAppEnvRejectsInvalidLines(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "missing equals", body: "NO_EQUALS\n", want: "invalid env line"},
		{name: "bad key", body: "1BAD=value\n", want: "invalid env key"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("SINGLESERVER_STATE_DIR", dir)
			envDir := filepath.Join(dir, "env")
			if err := os.MkdirAll(envDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(envDir, "my-app.env"), []byte(test.body), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := loadAppEnv("my-app")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
		})
	}
}
