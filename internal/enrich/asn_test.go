package enrich

import (
	"bytes"
	"compress/gzip"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

const sample = "1.0.0.0\t1.0.0.255\t13335\tUS\tCLOUDFLARENET\n" +
	"1.0.1.0\t1.0.3.255\t0\tNone\tNot routed\n" +
	"49.204.0.0\t49.207.255.255\t24309\tIN\tCABLELITE-AS-AP Atria Convergence Technologies Pvt. Ltd.\n" +
	"8.8.8.0\t8.8.8.255\t15169\tUS\tGOOGLE\n"

func TestLookup(t *testing.T) {
	db, err := Load(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if db.Len() != 3 {
		t.Fatalf("expected 3 routed ranges, got %d", db.Len())
	}
	cases := map[string]int{"1.0.0.1": 13335, "49.205.1.1": 24309, "8.8.8.8": 15169, "1.0.2.1": 0, "9.9.9.9": 0}
	for ip, want := range cases {
		as, ok := db.Lookup(netip.MustParseAddr(ip))
		if (want != 0) != ok || as.Number != want {
			t.Errorf("%s: got %d ok=%v, want %d", ip, as.Number, ok, want)
		}
	}
}

func TestLoadGzipFileAndErrors(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(sample))
	_ = zw.Close()
	path := filepath.Join(t.TempDir(), "ip2asn-v4.tsv.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if db, err := LoadFile(path); err != nil || db.Len() != 3 {
		t.Fatalf("gzip load: %v", err)
	}
	if _, err := Load(strings.NewReader("garbage\n")); err == nil {
		t.Fatal("malformed file accepted")
	}
	if _, err := Load(strings.NewReader("")); err != ErrNoData {
		t.Fatalf("empty file: %v", err)
	}
}

func TestScopeAndAnnotate(t *testing.T) {
	for ip, want := range map[string]Scope{
		"192.168.1.1": ScopeLAN, "10.2.3.4": ScopeLAN, "172.20.0.1": ScopeLAN,
		"100.72.0.1": ScopeCGNAT, "127.0.0.1": ScopeLoopback, "49.205.1.1": ScopePublic, "169.254.1.1": ScopeOther,
	} {
		if got := ScopeOf(netip.MustParseAddr(ip)); got != want {
			t.Errorf("%s: got %s want %s", ip, got, want)
		}
	}
	db, _ := Load(strings.NewReader(sample))
	hops := []wire.Hop{{TTL: 1, Addr: "192.168.1.1"}, {TTL: 2, Addr: "100.72.0.1"}, {TTL: 3, Addr: "49.205.1.1", ASN: 999}, {TTL: 4}}
	db.AnnotateHops(hops)
	if hops[0].Scope != "lan" || hops[1].Scope != "cgnat" || hops[2].ASN != 24309 || hops[3].Scope != "" {
		t.Fatalf("annotation wrong: %+v", hops)
	}
	var nilDB *DB
	nilDB.AnnotateHops(hops) // scopes still work without ASN data
	if hops[2].Scope != "public" || hops[2].ASN != 0 {
		t.Fatalf("nil DB annotation wrong: %+v", hops[2])
	}
}
