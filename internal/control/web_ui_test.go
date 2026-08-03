package control

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestDashboardUsesGuidedOperatorLanguage(t *testing.T) {
	htmlBytes, err := dashboardFS.ReadFile("web/dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlBytes)
	required := []string{
		`id="app"`,
		`type="module"`,
		`/assets/`,
	}
	for _, text := range required {
		if !strings.Contains(html, text) {
			t.Fatalf("dashboard is missing guided UI text %q", text)
		}
	}
	request := httptest.NewRequest("GET", "/", nil)
	response := httptest.NewRecorder()
	(&Server{}).dashboard(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `id="app"`) {
		t.Fatalf("dashboard root was not served: status=%d", response.Code)
	}
	if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "no-cache" {
		t.Fatalf("dashboard root cache control = %q", cacheControl)
	}

	match := regexp.MustCompile(`src="(/assets/[^"]+)"`).FindStringSubmatch(html)
	if len(match) != 2 {
		t.Fatal("dashboard does not reference a JavaScript asset")
	}
	request = httptest.NewRequest("GET", match[1], nil)
	response = httptest.NewRecorder()
	(&Server{}).dashboard(response, request)
	if response.Code != 200 {
		t.Fatalf("dashboard asset was not served: status=%d", response.Code)
	}
	if contentType := response.Header().Get("Content-Type"); !strings.Contains(contentType, "javascript") {
		t.Fatalf("dashboard JavaScript asset has unexpected content type %q", contentType)
	}
	if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "public, max-age=31536000, immutable" {
		t.Fatalf("dashboard asset cache control = %q", cacheControl)
	}
}
