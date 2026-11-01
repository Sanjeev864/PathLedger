// Package enrich maps IP addresses to the networks that operate them, so a
// traceroute hop can be attributed to "your LAN", "your ISP" or "upstream".
//
// Data comes from IPtoASN (https://iptoasn.com), which is free and published
// under the Public Domain Dedication and License. Download it with
// scripts/fetch-asn-db.sh.
package enrich

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

// Scope describes where an address sits relative to a customer site.
type Scope string

// Address scopes.
const (
	ScopeLAN      Scope = "lan"      // RFC 1918 private space: the customer's own network
	ScopeCGNAT    Scope = "cgnat"    // RFC 6598 shared space: inside the ISP's carrier-grade NAT
	ScopeLoopback Scope = "loopback" // the agent host itself
	ScopePublic   Scope = "public"
	ScopeOther    Scope = "other" // link-local, documentation, multicast, unparsable
)

var (
	rfc1918 = []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
	}
	cgnat = netip.MustParsePrefix("100.64.0.0/10")
)

// ScopeOf classifies an address.
func ScopeOf(a netip.Addr) Scope {
	a = a.Unmap()
	switch {
	case !a.IsValid():
		return ScopeOther
	case a.IsLoopback():
		return ScopeLoopback
	case cgnat.Contains(a):
		return ScopeCGNAT
	case a.IsLinkLocalUnicast(), a.IsMulticast(), a.IsUnspecified():
		return ScopeOther
	}
	for _, p := range rfc1918 {
		if p.Contains(a) {
			return ScopeLAN
		}
	}
	if a.IsPrivate() { // IPv6 ULA
		return ScopeLAN
	}
	return ScopePublic
}

// AS is an autonomous system.
type AS struct {
	Number  int
	Name    string
	Country string
}

type asRange struct {
	start, end uint32
	as         int32 // index into DB.systems
}

// DB answers IPv4 address → AS lookups from sorted, non-overlapping ranges.
type DB struct {
	ranges  []asRange
	systems []AS
}

// ErrNoData is returned by Load when the file has no usable rows.
var ErrNoData = errors.New("no ASN data")

// LoadFile reads an IPtoASN ip2asn-v4.tsv file (optionally gzip-compressed).
func LoadFile(path string) (*DB, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		zr, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		defer zr.Close()
		r = zr
	}
	return Load(r)
}

// Load parses IPtoASN TSV rows: range_start, range_end, AS_number,
// country_code, AS_description. Rows for AS 0 ("Not routed") are skipped.
func Load(r io.Reader) (*DB, error) {
	db := &DB{}
	index := map[int]int32{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 5 {
			return nil, fmt.Errorf("line %d: expected 5 tab-separated fields", line)
		}
		start, err1 := netip.ParseAddr(f[0])
		end, err2 := netip.ParseAddr(f[1])
		num, err3 := strconv.Atoi(f[2])
		if err1 != nil || err2 != nil || err3 != nil || !start.Is4() || !end.Is4() {
			return nil, fmt.Errorf("line %d: malformed row", line)
		}
		if num == 0 {
			continue
		}
		idx, ok := index[num]
		if !ok {
			idx = int32(len(db.systems))
			index[num] = idx
			db.systems = append(db.systems, AS{Number: num, Country: f[3], Name: f[4]})
		}
		db.ranges = append(db.ranges, asRange{start: v4(start), end: v4(end), as: idx})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(db.ranges) == 0 {
		return nil, ErrNoData
	}
	sort.Slice(db.ranges, func(i, j int) bool { return db.ranges[i].start < db.ranges[j].start })
	return db, nil
}

func v4(a netip.Addr) uint32 {
	b := a.As4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// Lookup returns the AS announcing a public IPv4 address.
func (db *DB) Lookup(a netip.Addr) (AS, bool) {
	if db == nil {
		return AS{}, false
	}
	a = a.Unmap()
	if !a.Is4() {
		return AS{}, false
	}
	x := v4(a)
	i := sort.Search(len(db.ranges), func(i int) bool { return db.ranges[i].start > x }) - 1
	if i < 0 || x > db.ranges[i].end {
		return AS{}, false
	}
	return db.systems[db.ranges[i].as], true
}

// Len reports the number of ranges loaded.
func (db *DB) Len() int {
	if db == nil {
		return 0
	}
	return len(db.ranges)
}

// AnnotateHops fills in each hop's scope and, for public addresses, its AS.
// Values sent by the agent are overwritten: the controller is the authority.
func (db *DB) AnnotateHops(hops []wire.Hop) {
	for i := range hops {
		h := &hops[i]
		h.Scope, h.ASN, h.ASName = "", 0, ""
		a, err := netip.ParseAddr(h.Addr)
		if err != nil {
			continue
		}
		h.Scope = string(ScopeOf(a))
		if h.Scope == string(ScopePublic) {
			if as, ok := db.Lookup(a); ok {
				h.ASN, h.ASName = as.Number, as.Name
			}
		}
	}
}
