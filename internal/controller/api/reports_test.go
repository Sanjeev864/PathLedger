package api_test

import (
	"testing"
	"time"
)

func TestSLAReportAPI(t *testing.T) {
	h, newKey := newHarness(t)
	var site idResp
	if code := h.call("POST", "/api/v1/sites", h.apiKey, map[string]string{"name": "HQ"}, &site); code != 201 {
		t.Fatalf("site: %d", code)
	}
	var rep struct {
		TenantName string `json:"tenant_name"`
		Period     string `json:"period"`
		TimeZone   string `json:"time_zone"`
		Partial    bool   `json:"partial"`
		Sites      []struct {
			SiteID          string   `json:"site_id"`
			AvailabilityPct *float64 `json:"availability_pct"`
		} `json:"sites"`
		Incidents []any `json:"incidents"`
	}
	if code := h.call("GET", "/api/v1/reports/sla?tz=Asia/Kolkata", h.apiKey, nil, &rep); code != 200 {
		t.Fatalf("report: %d", code)
	}
	ist, _ := time.LoadLocation("Asia/Kolkata")
	if rep.TenantName != "Acme" || rep.Period != time.Now().In(ist).Format("2006-01") || rep.TimeZone != "Asia/Kolkata" ||
		!rep.Partial || len(rep.Sites) != 1 || rep.Sites[0].SiteID != site.ID || rep.Incidents == nil {
		t.Fatalf("report: %+v", rep)
	}

	// Last month: the site did not exist yet.
	last := time.Now().UTC().AddDate(0, -1, 0).Format("2006-01")
	if code := h.call("GET", "/api/v1/reports/sla?month="+last, h.apiKey, nil, &rep); code != 200 || rep.Partial || len(rep.Sites) != 0 {
		t.Fatalf("last month: %d %+v", code, rep)
	}

	for _, q := range []string{"month=2026-13", "month=oct", "tz=Mars/Base", "month=2999-01", "month=2001-01"} {
		if code := h.call("GET", "/api/v1/reports/sla?"+q, h.apiKey, nil, nil); code != 400 {
			t.Fatalf("%s: %d", q, code)
		}
	}

	// Another tenant sees only its own sites.
	if code := h.call("GET", "/api/v1/reports/sla", newKey("Globex"), nil, &rep); code != 200 || len(rep.Sites) != 0 || rep.TenantName != "Globex" {
		t.Fatalf("other tenant: %d %+v", code, rep)
	}
	if code := h.call("GET", "/api/v1/reports/sla", "", nil, nil); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
}
