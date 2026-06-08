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

func TestDNSRecordContentMatchesTunnelTarget(t *testing.T) {
	if !dnsRecordContentMatches("ABC.cfargotunnel.com.", "abc.cfargotunnel.com") {
		t.Fatal("expected case-insensitive trailing-dot match")
	}
	if dnsRecordContentMatches("other.cfargotunnel.com", "abc.cfargotunnel.com") {
		t.Fatal("did not expect different tunnel target to match")
	}
	if dnsRecordContentMatches("", "abc.cfargotunnel.com") {
		t.Fatal("did not expect empty content to match")
	}
}
