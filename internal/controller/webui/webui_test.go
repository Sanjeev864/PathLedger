package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	h := Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/sites/whatever", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<html") && !strings.Contains(rec.Body.String(), "<!doctype") {
		t.Fatalf("app shell not served: %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatal("missing CSP")
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST allowed: %d", rec.Code)
	}
}
