package singleserver

import (
	"os"
	"path/filepath"
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
