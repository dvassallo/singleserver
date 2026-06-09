package singleserver

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

func TestEnsureCloudflaredConfigPreservesAppRoutes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cloudflared.yml")
	if err := os.WriteFile(path, []byte(`tunnel: old
credentials-file: /old.json
ingress:
  - hostname: app.example.com
    service: http://127.0.0.1:80
  - service: http_status:404
`), 0600); err != nil {
		t.Fatal(err)
	}

	if err := ensureCloudflaredConfig(path, "new-tunnel", "/new.json"); err != nil {
		t.Fatal(err)
	}
	config, err := readCloudflaredConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config.Tunnel != "new-tunnel" || config.CredentialsFile != "/new.json" {
		t.Fatalf("unexpected tunnel config: %#v", config)
	}
	routes := cloudflaredRoutes(config)
	if routes["app.example.com"] != "http://127.0.0.1:80" {
		t.Fatalf("app route not preserved: %#v", routes)
	}
	if got := config.Ingress[len(config.Ingress)-1]; got.Hostname != "" || got.Service != "http_status:404" {
		t.Fatalf("expected fallback route last, got %#v", got)
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

func TestSyncCloudflareAddRollsBackRouteWhenDNSFails(t *testing.T) {
	state := &CloudflareState{TunnelID: "tunnel"}
	calls := []string{}
	ops := cloudflareDomainSyncOps{
		ensureRoute: func(hostname string) error {
			calls = append(calls, "ensure:"+hostname)
			return nil
		},
		upsertRecord: func(hostname string) error {
			calls = append(calls, "upsert:"+hostname)
			return errors.New("dns failed")
		},
		removeRoute: func(hostname string) error {
			calls = append(calls, "remove:"+hostname)
			return nil
		},
		deleteRecord: func(hostname string) error {
			calls = append(calls, "delete:"+hostname)
			return nil
		},
		restart: func() error {
			calls = append(calls, "restart")
			return nil
		},
	}

	var out bytes.Buffer
	err := syncCloudflareAppDomainWithOps("app.example.com", true, &out, state, ops)
	if err == nil || !strings.Contains(err.Error(), "dns failed") {
		t.Fatalf("expected dns failure, got %v", err)
	}
	want := []string{"ensure:app.example.com", "upsert:app.example.com", "remove:app.example.com"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	if strings.Contains(out.String(), "domain\tok") {
		t.Fatalf("unexpected success output: %s", out.String())
	}
}

func TestSyncCloudflareAddTunnelDomain(t *testing.T) {
	state := &CloudflareState{TunnelID: "tunnel"}
	calls := []string{}
	ops := cloudflareDomainSyncOps{
		target: "tunnel.cfargotunnel.com",
		upsertRecord: func(hostname string) error {
			calls = append(calls, "upsert:"+hostname)
			return nil
		},
		deleteRecord: func(hostname string) error {
			calls = append(calls, "delete:"+hostname)
			return nil
		},
		ensureRoute: func(hostname string) error {
			calls = append(calls, "ensure:"+hostname)
			return nil
		},
		removeRoute: func(hostname string) error {
			calls = append(calls, "remove:"+hostname)
			return nil
		},
		restart: func() error {
			calls = append(calls, "restart")
			return nil
		},
	}

	var out bytes.Buffer
	if err := syncCloudflareAppDomainWithOps("app.example.com", true, &out, state, ops); err != nil {
		t.Fatal(err)
	}
	want := []string{"ensure:app.example.com", "upsert:app.example.com", "restart"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	if !strings.Contains(out.String(), "cloudflare\tdomain\tok\tapp.example.com -> tunnel.cfargotunnel.com") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestSyncCloudflareRemoveRollsBackRouteWhenDNSFails(t *testing.T) {
	state := &CloudflareState{TunnelID: "tunnel"}
	calls := []string{}
	ops := cloudflareDomainSyncOps{
		ensureRoute: func(hostname string) error {
			calls = append(calls, "ensure:"+hostname)
			return nil
		},
		upsertRecord: func(hostname string) error {
			calls = append(calls, "upsert:"+hostname)
			return nil
		},
		removeRoute: func(hostname string) error {
			calls = append(calls, "remove:"+hostname)
			return nil
		},
		deleteRecord: func(hostname string) error {
			calls = append(calls, "delete:"+hostname)
			return errors.New("dns failed")
		},
		restart: func() error {
			calls = append(calls, "restart")
			return nil
		},
	}

	var out bytes.Buffer
	err := syncCloudflareAppDomainWithOps("app.example.com", false, &out, state, ops)
	if err == nil || !strings.Contains(err.Error(), "dns failed") {
		t.Fatalf("expected dns failure, got %v", err)
	}
	want := []string{"remove:app.example.com", "delete:app.example.com", "ensure:app.example.com"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	if strings.Contains(out.String(), "domain\tok") {
		t.Fatalf("unexpected success output: %s", out.String())
	}
}
