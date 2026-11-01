package wire

import (
	"strings"
	"testing"
	"time"
)

func validTest(kind ProbeKind, target string) Test {
	return Test{Kind: kind, Target: target, IntervalSeconds: 60, TimeoutMillis: 2000}
}

func TestTestValidate(t *testing.T) {
	cases := []struct {
		name    string
		test    Test
		wantErr string
	}{
		{"icmp ip", validTest(KindICMP, "1.1.1.1"), ""},
		{"icmp host", validTest(KindICMP, "example.com"), ""},
		{"icmp bad host", validTest(KindICMP, "exa mple.com"), "target"},
		{"dns domain", validTest(KindDNS, "example.com"), ""},
		{"dns rejects ip", validTest(KindDNS, "8.8.8.8"), "target"},
		{"tcp host port", validTest(KindTCP, "example.com:443"), ""},
		{"tcp ipv6", validTest(KindTCP, "[2606:4700::1111]:443"), ""},
		{"tcp missing port", validTest(KindTCP, "example.com"), "target"},
		{"tcp bad port", validTest(KindTCP, "example.com:70000"), "target"},
		{"tls ok", validTest(KindTLS, "example.com:443"), ""},
		{"http ok", validTest(KindHTTP, "https://example.com/health"), ""},
		{"http bad scheme", validTest(KindHTTP, "ftp://example.com"), "target"},
		{"http credentials", validTest(KindHTTP, "https://u:p@example.com"), "target"},
		{"unknown kind", validTest("smtp", "example.com"), "kind"},
		{"empty target", validTest(KindICMP, ""), "target"},
		{"interval too short", Test{Kind: KindICMP, Target: "1.1.1.1", IntervalSeconds: 5, TimeoutMillis: 1000}, "interval_seconds"},
		{"timeout too long", Test{Kind: KindICMP, Target: "1.1.1.1", IntervalSeconds: 60, TimeoutMillis: 60_000}, "timeout_millis"},
		{"timeout not below interval", Test{Kind: KindICMP, Target: "1.1.1.1", IntervalSeconds: 10, TimeoutMillis: 10_000}, "timeout_millis"},
		{"count on dns", func() Test { t := validTest(KindDNS, "example.com"); t.Params.Count = 3; return t }(), "params.count"},
		{"count too high", func() Test { t := validTest(KindICMP, "1.1.1.1"); t.Params.Count = 50; return t }(), "params.count"},
		{"resolver ok", func() Test { t := validTest(KindDNS, "example.com"); t.Params.Resolver = "1.1.1.1:53"; return t }(), ""},
		{"resolver no port", func() Test { t := validTest(KindDNS, "example.com"); t.Params.Resolver = "1.1.1.1"; return t }(), "params.resolver"},
		{"expect status ok", func() Test { t := validTest(KindHTTP, "https://x.io"); t.Params.ExpectStatus = 204; return t }(), ""},
		{"expect status bad", func() Test { t := validTest(KindHTTP, "https://x.io"); t.Params.ExpectStatus = 42; return t }(), "params.expect_status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.test.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error on %s, got nil", tc.wantErr)
			}
			if !IsValidationError(err) || !strings.HasPrefix(err.Error(), tc.wantErr) {
				t.Fatalf("expected %s error, got %v", tc.wantErr, err)
			}
		})
	}
}

func TestResultValidate(t *testing.T) {
	ok := Result{TestID: "t1", Time: time.Now(), Kind: KindTCP, Target: "a:1", Success: true, LatencyMS: Float(12)}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}
	bad := ok
	bad.LossPct = Float(150)
	if err := bad.Validate(); err == nil {
		t.Fatal("loss above 100 accepted")
	}
	bad = ok
	bad.Time = time.Time{}
	if err := bad.Validate(); err == nil {
		t.Fatal("zero time accepted")
	}
	bad = ok
	bad.Details = map[string]string{"k": strings.Repeat("x", MaxDetailValueLen+1)}
	if err := bad.Validate(); err == nil {
		t.Fatal("oversized detail accepted")
	}
}
