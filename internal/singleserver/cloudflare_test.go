package singleserver

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteCloudflaredCredentialsRequiresSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	err := writeCloudflaredCredentials(path, &CloudflareState{
		AccountID: "account",
		TunnelID:  "tunnel",
	})
	if err == nil {
		t.Fatal("expected missing tunnel secret error")
	}
	if !strings.Contains(err.Error(), "tunnel secret") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWriteCloudflaredCredentialsWritesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	err := writeCloudflaredCredentials(path, &CloudflareState{
		AccountID:    "account",
		TunnelID:     "tunnel",
		TunnelSecret: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
}
