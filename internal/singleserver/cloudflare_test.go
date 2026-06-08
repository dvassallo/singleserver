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

func TestConflictingCNAMERecord(t *testing.T) {
	target := "abc.cfargotunnel.com"
	if conflict := conflictingCNAMERecord([]cloudflareDNSRecord{
		{ID: "1", Content: "ABC.cfargotunnel.com."},
	}, target); conflict != nil {
		t.Fatalf("did not expect matching target to conflict: %#v", conflict)
	}

	conflict := conflictingCNAMERecord([]cloudflareDNSRecord{
		{ID: "1", Content: "old.example.net"},
		{ID: "2", Content: "abc.cfargotunnel.com"},
	}, target)
	if conflict == nil {
		t.Fatal("expected conflicting CNAME")
	}
	if conflict.ID != "1" {
		t.Fatalf("unexpected conflict: %#v", conflict)
	}
}
