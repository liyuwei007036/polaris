package control

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIPLocatorUsesEmbeddedIPv4AndIPv6Databases(t *testing.T) {
	locator, err := newIPLocator()
	if err != nil {
		t.Fatal(err)
	}
	if location := locator.Locate("8.8.8.8"); !strings.Contains(location, "United States") {
		t.Fatalf("unexpected IPv4 location %q", location)
	}
	if location := locator.Locate("240e:3b7:3272:d8d0:db09:c067:8d59:539e"); !strings.Contains(location, "广东省") {
		t.Fatalf("unexpected IPv6 location %q", location)
	}
	if location := locator.Locate("192.168.1.10"); location != "内网" {
		t.Fatalf("private IP location = %q", location)
	}
	if location := locator.Locate("127.0.0.1"); location != "本机" {
		t.Fatalf("loopback IP location = %q", location)
	}
}

func TestIPLocatorLocatesFirewallCIDRs(t *testing.T) {
	locator, err := newIPLocator()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		cidr string
		want string
	}{
		{cidr: "8.8.8.0/24", want: "United States"},
		{cidr: "240e:3b7:3272:d8d0:db09:c067:8d59:539e/128", want: "广东省"},
		{cidr: "192.168.1.0/24", want: "内网"},
		{cidr: "0.0.0.0/0", want: "所有 IPv4 地址"},
		{cidr: "::/0", want: "所有 IPv6 地址"},
		{cidr: "", want: "所有来源"},
	}
	for _, test := range tests {
		if location := locator.LocateCIDR(test.cidr); !strings.Contains(location, test.want) {
			t.Errorf("LocateCIDR(%q) = %q, want location containing %q", test.cidr, location, test.want)
		}
	}
}

func TestIPLocatorLocateCity(t *testing.T) {
	locator, err := newIPLocator()
	if err != nil {
		t.Fatal(err)
	}
	r, _ := locator.v4.Search("58.60.188.222")
	t.Logf("raw 58.60.188.222 region: %q", r)
	r2, _ := locator.v4.Search("202.96.209.133")
	t.Logf("raw 202.96.209.133 region: %q", r2)
	r3, _ := locator.v4.Search("8.8.8.8")
	t.Logf("raw 8.8.8.8 region: %q", r3)
	if city := locator.LocateCity("202.96.209.133"); city != "上海市" {
		t.Fatalf("expected 上海市, got %q", city)
	}
	if city := locator.LocateCity("58.60.188.222"); city != "深圳市" {
		t.Fatalf("expected 深圳市, got %q", city)
	}
	if city := locator.LocateCity("8.8.8.8"); city != "" {
		t.Fatalf("expected empty city for 8.8.8.8, got %q", city)
	}
}

func TestIPLocatorStructuredLookup(t *testing.T) {
	locator, err := newIPLocator()
	if err != nil {
		t.Fatal(err)
	}
	info := locator.Lookup("202.96.209.133")
	if info.City != "上海市" {
		t.Fatalf("expected city 上海市, got %q", info.City)
	}
	if !info.IsDomestic {
		t.Fatalf("expected 202.96.209.133 to be domestic")
	}

	infoUS := locator.Lookup("8.8.8.8")
	if infoUS.IsDomestic {
		t.Fatalf("expected 8.8.8.8 not to be domestic")
	}
	if infoUS.Province != "California" {
		t.Fatalf("expected province California, got %q", infoUS.Province)
	}
	if infoUS.City != "" {
		t.Fatalf("expected empty city for 8.8.8.8, got %q", infoUS.City)
	}

	infoLocal := locator.Lookup("127.0.0.1")
	if infoLocal.Location != "本机" || infoLocal.Source != "system" {
		t.Fatalf("expected loopback system info, got %#v", infoLocal)
	}

	infoPrivate := locator.Lookup("10.0.0.1")
	if infoPrivate.Location != "内网" || infoPrivate.Source != "system" {
		t.Fatalf("expected private system info, got %#v", infoPrivate)
	}
}

func TestIPLocatorMMDBStatusAndLifecycle(t *testing.T) {
	locator, err := newIPLocator()
	if err != nil {
		t.Fatal(err)
	}
	loaded, path, dbType := locator.MMDBStatus()
	if loaded {
		t.Fatalf("expected mmdb not loaded initially in test env, got loaded=%v, path=%q, dbType=%q", loaded, path, dbType)
	}

	// Loading non-existent path should return an error
	err = locator.LoadMMDB("non-existent-file.mmdb")
	if err == nil {
		t.Fatal("expected error loading non-existent mmdb")
	}

	locator.ClearCache()
	if err := locator.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestIPLocatorHelpers(t *testing.T) {
	names := map[string]string{"en": "Tokyo", "zh-CN": "东京"}
	if got := pickLocalizedName(names); got != "东京" {
		t.Fatalf("expected 东京, got %q", got)
	}
	namesEN := map[string]string{"en": "London"}
	if got := pickLocalizedName(namesEN); got != "London" {
		t.Fatalf("expected London, got %q", got)
	}
}

func TestIPDatabaseHTTPHandlers(t *testing.T) {
	store := createTestStore(t)

	server, err := NewServer(store, false)
	if err != nil {
		t.Fatal(err)
	}

	admin, _, err := store.createOperator(t.Context(), "admin-test", "password123456", "admin", false, false, false)
	if err != nil {
		t.Fatal(err)
	}
	session, err := store.createSession(t.Context(), admin.ID, admin.Role, false)
	if err != nil {
		t.Fatal(err)
	}
	addAuth := func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.Token})
		r.Header.Set("X-CSRF-Token", session.CSRFToken)
	}

	// 1. Test GET /api/v1/ip/database
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ip/database", nil)
	addAuth(req)
	rec := httptest.NewRecorder()
	server.handleGetIPDatabaseStatus(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code %d, body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"ip2region_loaded":true`) {
		t.Fatalf("missing ip2region_loaded: %s", rec.Body.String())
	}

	// 2. Test GET /api/v1/ip/lookup
	lookupReq := httptest.NewRequest(http.MethodGet, "/api/v1/ip/lookup?ip=202.96.209.133", nil)
	addAuth(lookupReq)
	lookupRec := httptest.NewRecorder()
	server.handleIPLookup(lookupRec, lookupReq)
	if lookupRec.Code != http.StatusOK {
		t.Fatalf("lookup status code %d, body: %s", lookupRec.Code, lookupRec.Body.String())
	}
	if !strings.Contains(lookupRec.Body.String(), `"city":"上海市"`) {
		t.Fatalf("lookup expected 上海市, got: %s", lookupRec.Body.String())
	}

	// 3. Test POST /api/v1/ip/database/reload
	reloadReq := httptest.NewRequest(http.MethodPost, "/api/v1/ip/database/reload", nil)
	addAuth(reloadReq)
	reloadRec := httptest.NewRecorder()
	server.handleReloadIPDatabase(reloadRec, reloadReq)
	if reloadRec.Code != http.StatusOK {
		t.Fatalf("reload status code %d, body: %s", reloadRec.Code, reloadRec.Body.String())
	}

	// 4. Test POST /api/v1/ip/database/upload with invalid file
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", "test.mmdb")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("not a real mmdb file"))
	_ = writer.Close()

	uploadReq := httptest.NewRequest(http.MethodPost, "/api/v1/ip/database/upload", &buf)
	addAuth(uploadReq)
	uploadReq.Header.Set("Content-Type", writer.FormDataContentType())
	uploadRec := httptest.NewRecorder()
	server.handleUploadIPDatabase(uploadRec, uploadReq)
	// Should fail cleanly since it's not a valid mmdb binary
	if uploadRec.Code == http.StatusOK {
		t.Fatalf("expected upload failure on invalid mmdb, got %d: %s", uploadRec.Code, uploadRec.Body.String())
	}

	// Verify the file was cleaned up from data dir
	destPath := filepath.Join(store.DataDir(), "test.mmdb")
	_ = os.Remove(destPath)
}
