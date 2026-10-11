package singleserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
)

// fakeInfo selects the paging fields that the fake puts in result_info.
type fakeInfo int

const (
	infoTotalPages fakeInfo = iota // total_pages and total_count
	infoTotalCount                 // total_count only
	infoNone                       // neither
)

// fakeCloudflareList serves items the way a Cloudflare list endpoint does:
// it honors page and per_page, caps per_page at maxPerPage, and filters on
// the name query parameter.
func fakeCloudflareList(t *testing.T, path string, items []map[string]string, maxPerPage int, mode fakeInfo, requests *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(fakeCloudflareListHandler(t, path, items, maxPerPage, mode, requests))
}

func fakeCloudflareListHandler(t *testing.T, path string, items []map[string]string, maxPerPage int, mode fakeInfo, requests *int32) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			t.Errorf("unexpected path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		atomic.AddInt32(requests, 1)
		query := r.URL.Query()
		page, _ := strconv.Atoi(query.Get("page"))
		if page < 1 {
			page = 1
		}
		perPage, _ := strconv.Atoi(query.Get("per_page"))
		if perPage < 1 {
			perPage = 20
		}
		if perPage > maxPerPage {
			perPage = maxPerPage
		}
		filtered := items
		if name := query.Get("name"); name != "" {
			filtered = nil
			for _, item := range items {
				if item["name"] == name {
					filtered = append(filtered, item)
				}
			}
		}
		start := (page - 1) * perPage
		if start > len(filtered) {
			start = len(filtered)
		}
		end := start + perPage
		if end > len(filtered) {
			end = len(filtered)
		}
		info := map[string]int{"page": page, "per_page": perPage, "count": end - start}
		if mode == infoTotalPages || mode == infoTotalCount {
			info["total_count"] = len(filtered)
		}
		if mode == infoTotalPages {
			info["total_pages"] = (len(filtered) + perPage - 1) / perPage
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":     true,
			"result":      filtered[start:end],
			"result_info": info,
		})
	}
}

func testCloudflareClient(t *testing.T, server *httptest.Server) *CloudflareClient {
	t.Helper()
	client, err := newCloudflareClient("test-token")
	if err != nil {
		t.Fatal(err)
	}
	client.baseURL = server.URL
	return client
}

// testZones returns count zones in alphabetical order. The last two are
// silepilates.com and specquick.com, as in the account that found the bug.
func testZones(count int) []map[string]string {
	zones := make([]map[string]string, 0, count)
	for i := 0; i < count-2; i++ {
		zones = append(zones, map[string]string{"id": fmt.Sprintf("zone-%02d", i), "name": fmt.Sprintf("a%02d.example", i)})
	}
	zones = append(zones,
		map[string]string{"id": "zone-silepilates", "name": "silepilates.com"},
		map[string]string{"id": "zone-specquick", "name": "specquick.com"},
	)
	return zones
}

func TestCloudflareZonesFollowsPages(t *testing.T) {
	var requests int32
	server := fakeCloudflareList(t, "/zones", testZones(22), 20, infoTotalPages, &requests)
	defer server.Close()
	client := testCloudflareClient(t, server)

	zones, err := client.allZones()
	if err != nil {
		t.Fatal(err)
	}
	if len(zones) != 22 {
		t.Fatalf("expected 22 zones, got %d", len(zones))
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("expected 2 requests, got %d", got)
	}

	zone, err := client.zoneForHostname("dev.specquick.com")
	if err != nil {
		t.Fatal(err)
	}
	if zone.ID != "zone-specquick" {
		t.Fatalf("unexpected zone: %#v", zone)
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("expected cached zones, got %d requests", got)
	}
}

func TestCloudflareZonesRequestsLargePages(t *testing.T) {
	var requests int32
	server := fakeCloudflareList(t, "/zones", testZones(101), 50, infoTotalPages, &requests)
	defer server.Close()

	zones, err := testCloudflareClient(t, server).allZones()
	if err != nil {
		t.Fatal(err)
	}
	if len(zones) != 101 {
		t.Fatalf("expected 101 zones, got %d", len(zones))
	}
	if got := atomic.LoadInt32(&requests); got != 3 {
		t.Fatalf("expected 3 requests of 50 zones, got %d", got)
	}
}

func TestCloudflareZonesStopsAtShortPageWithoutTotalPages(t *testing.T) {
	var requests int32
	server := fakeCloudflareList(t, "/zones", testZones(75), 50, infoNone, &requests)
	defer server.Close()

	zones, err := testCloudflareClient(t, server).allZones()
	if err != nil {
		t.Fatal(err)
	}
	if len(zones) != 75 {
		t.Fatalf("expected 75 zones, got %d", len(zones))
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("expected 2 requests, got %d", got)
	}
}

func TestCloudflareZonesStopsAtTotalCountWithoutTotalPages(t *testing.T) {
	var requests int32
	server := fakeCloudflareList(t, "/zones", testZones(100), 50, infoTotalCount, &requests)
	defer server.Close()

	zones, err := testCloudflareClient(t, server).allZones()
	if err != nil {
		t.Fatal(err)
	}
	if len(zones) != 100 {
		t.Fatalf("expected 100 zones, got %d", len(zones))
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("expected 2 requests, got %d", got)
	}
}

func TestCloudflareZonesNameLookup(t *testing.T) {
	var requests int32
	server := fakeCloudflareList(t, "/zones", testZones(22), 20, infoTotalPages, &requests)
	defer server.Close()

	zones, err := testCloudflareClient(t, server).zones("specquick.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(zones) != 1 || zones[0].ID != "zone-specquick" {
		t.Fatalf("unexpected zones: %#v", zones)
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("expected 1 request, got %d", got)
	}
}

func TestCloudflareDNSRecordsFollowsPages(t *testing.T) {
	records := make([]map[string]string, 0, 23)
	for i := 0; i < 23; i++ {
		records = append(records, map[string]string{"id": fmt.Sprintf("record-%02d", i), "type": "CNAME", "name": "app.example.com", "content": "target.example.com"})
	}
	var requests int32
	server := fakeCloudflareList(t, "/zones/zone-1/dns_records", records, 20, infoTotalPages, &requests)
	defer server.Close()

	got, err := testCloudflareClient(t, server).dnsRecords("zone-1", "app.example.com", "CNAME")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 23 {
		t.Fatalf("expected 23 records, got %d", len(got))
	}
}

func TestCloudflareFindTunnelFollowsPages(t *testing.T) {
	tunnels := make([]map[string]string, 0, 21)
	for i := 0; i < 20; i++ {
		tunnels = append(tunnels, map[string]string{"id": fmt.Sprintf("tunnel-%02d", i), "name": fmt.Sprintf("other-%02d", i)})
	}
	tunnels = append(tunnels, map[string]string{"id": "tunnel-singleserver", "name": "singleserver"})
	var requests int32
	list := fakeCloudflareListHandler(t, "/accounts/account-1/cfd_tunnel", tunnels, 20, infoTotalPages, &requests)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		if query.Get("is_deleted") != "false" || query.Get("name") != "singleserver" {
			t.Errorf("expected is_deleted and name filters, got %q", r.URL.RawQuery)
		}
		// Ignore the name filter so that the match is on page 2.
		query.Del("name")
		r.URL.RawQuery = query.Encode()
		list(w, r)
	}))
	defer server.Close()

	tunnel, err := testCloudflareClient(t, server).findTunnel("account-1", "singleserver")
	if err != nil {
		t.Fatal(err)
	}
	if tunnel == nil || tunnel.ID != "tunnel-singleserver" {
		t.Fatalf("unexpected tunnel: %#v", tunnel)
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("expected 2 requests, got %d", got)
	}
}
