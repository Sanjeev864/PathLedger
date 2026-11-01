package alert

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/analytics"
	"github.com/sanjeev864/pathledger/internal/controller/store"
	"github.com/sanjeev864/pathledger/internal/testutil/pgtest"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeSMTP accepts one connection at a time and records message bodies.
type fakeSMTP struct {
	mu   sync.Mutex
	msgs []string
	addr string
}

func startSMTP(t *testing.T) *fakeSMTP {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeSMTP{addr: ln.Addr().String()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			say("250 fake")
		case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"), strings.HasPrefix(cmd, "RSET"), strings.HasPrefix(cmd, "NOOP"):
			say("250 ok")
		case cmd == "DATA":
			say("354 go ahead")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.msgs = append(f.msgs, b.String())
			f.mu.Unlock()
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("502 unknown")
		}
	}
}

type hookServer struct {
	mu       sync.Mutex
	bodies   [][]byte
	sigs     []string
	status   int
	telegram []map[string]any
}

func newHookServer(t *testing.T) (*hookServer, *httptest.Server) {
	h := &hookServer{status: 200}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		defer h.mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/botTOKEN/sendMessage") {
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			h.telegram = append(h.telegram, m)
			w.WriteHeader(200)
			return
		}
		h.bodies = append(h.bodies, b)
		h.sigs = append(h.sigs, r.Header.Get("X-PathLedger-Signature"))
		w.WriteHeader(h.status)
	}))
	t.Cleanup(srv.Close)
	return h, srv
}

func TestEndToEndDelivery(t *testing.T) {
	ctx := context.Background()
	st := pgtest.New(t)
	tenant, _ := st.CreateTenant(ctx, "T")
	site, _ := st.CreateSite(ctx, tenant.ID, "Jayanagar", "")
	smtpSrv := startSMTP(t)
	hooks, srv := newHookServer(t)
	host, port, _ := net.SplitHostPort(smtpSrv.addr)
	portN := 0
	for _, c := range port {
		portN = portN*10 + int(c-'0')
	}

	for _, ch := range []store.Channel{
		{TenantID: tenant.ID, Kind: "webhook", Name: "ops hook", Config: map[string]any{"url": srv.URL + "/hook", "secret": "s3cret"}},
		{TenantID: tenant.ID, Kind: "telegram", Name: "ops chat", Config: map[string]any{"chat_id": "-1001"}},
		{TenantID: tenant.ID, Kind: "email", Name: "ops mail", Config: map[string]any{"to": []any{"noc@example.com"}}},
	} {
		if _, err := st.CreateChannel(ctx, ch); err != nil {
			t.Fatal(err)
		}
	}

	n := &Notifier{Store: st, Log: quiet, PublicURL: "https://app.pathledger.example"}
	ev := store.Event{ID: "11111111-1111-1111-1111-111111111111", TenantID: tenant.ID, SiteID: site.ID, Verdict: string(analytics.VerdictISP),
		Summary: "Every destination is affected.", Evidence: []string{"icmp 1.1.1.1 is down (timeout)"}, StartedAt: time.Now().UTC()}
	n.Notify(ctx, analytics.ChangeOpened, ev)

	d := &Dispatcher{Store: st, Log: quiet, Sender: Router{
		"webhook":  &Webhook{Client: SafeHTTPClient(true)},
		"telegram": &Telegram{Token: "TOKEN", BaseURL: srv.URL, Client: srv.Client()},
		"email":    &Email{Config: SMTPConfig{Host: host, Port: portN, From: "PathLedger <alerts@pathledger.example>"}},
	}}
	if n, err := d.Drain(ctx); err != nil || n != 3 {
		t.Fatalf("drain: %d %v", n, err)
	}

	hooks.mu.Lock()
	defer hooks.mu.Unlock()
	if len(hooks.bodies) != 1 {
		t.Fatalf("webhook calls: %d", len(hooks.bodies))
	}
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(hooks.bodies[0])
	if hooks.sigs[0] != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
		t.Fatal("webhook signature does not verify")
	}
	var p Payload
	_ = json.Unmarshal(hooks.bodies[0], &p)
	if p.Type != "event.opened" || p.SiteName != "Jayanagar" || !strings.HasSuffix(p.Link, "/#/events/"+ev.ID) {
		t.Fatalf("webhook payload: %+v", p)
	}
	if len(hooks.telegram) != 1 || hooks.telegram[0]["chat_id"] != "-1001" || !strings.Contains(hooks.telegram[0]["text"].(string), "Jayanagar") {
		t.Fatalf("telegram: %+v", hooks.telegram)
	}
	smtpSrv.mu.Lock()
	defer smtpSrv.mu.Unlock()
	if len(smtpSrv.msgs) != 1 || !strings.Contains(smtpSrv.msgs[0], "Subject: [PathLedger] Jayanagar") || !strings.Contains(smtpSrv.msgs[0], "Evidence:") {
		t.Fatalf("email: %q", smtpSrv.msgs)
	}
	statuses, _ := st.ListDeliveries(ctx, tenant.ID, 10)
	for _, s := range statuses {
		if s.DeliveredAt == nil {
			t.Fatalf("delivery not marked delivered: %+v", s)
		}
	}
}

func TestRetryAndPermanentFailure(t *testing.T) {
	ctx := context.Background()
	st := pgtest.New(t)
	tenant, _ := st.CreateTenant(ctx, "T")
	hooks, srv := newHookServer(t)
	ch, _ := st.CreateChannel(ctx, store.Channel{TenantID: tenant.ID, Kind: "webhook", Name: "h", Config: map[string]any{"url": srv.URL, "secret": "x"}})

	now := time.Now().UTC()
	d := &Dispatcher{Store: st, Log: quiet, Sender: Router{"webhook": &Webhook{Client: SafeHTTPClient(true)}}, now: func() time.Time { return now }}

	hooks.status = 503
	_, _ = st.EnqueueDeliveries(ctx, tenant.ID, ch.ID, Payload{Type: "test"})
	now = time.Now().UTC() // the delivery became due when it was queued
	_, _ = d.Drain(ctx)
	s, _ := st.ListDeliveries(ctx, tenant.ID, 1)
	if s[0].DeliveredAt != nil || s[0].FailedAt != nil || s[0].Attempts != 1 || !strings.Contains(s[0].LastError, "503") {
		t.Fatalf("expected scheduled retry, got %+v", s[0])
	}
	if n, _ := d.Drain(ctx); n != 0 {
		t.Fatal("retry ran before its scheduled time")
	}
	now = now.Add(2 * time.Minute)
	hooks.status = 200
	_, _ = d.Drain(ctx)
	if s, _ = st.ListDeliveries(ctx, tenant.ID, 1); s[0].DeliveredAt == nil || s[0].Attempts != 2 {
		t.Fatalf("retry should deliver: %+v", s[0])
	}

	hooks.status = 404
	_, _ = st.EnqueueDeliveries(ctx, tenant.ID, ch.ID, Payload{Type: "test"})
	now = time.Now().UTC().Add(time.Second)
	_, _ = d.Drain(ctx)
	if s, _ = st.ListDeliveries(ctx, tenant.ID, 1); s[0].FailedAt == nil {
		t.Fatalf("4xx must fail permanently: %+v", s[0])
	}
}

func TestSafeClientBlocksPrivateTargets(t *testing.T) {
	_, srv := newHookServer(t) // listens on 127.0.0.1
	err := (&Webhook{Client: SafeHTTPClient(false)}).Send(context.Background(),
		store.Channel{Kind: "webhook", Config: map[string]any{"url": srv.URL, "secret": "x"}}, Payload{Type: "test"})
	if !errors.Is(err, ErrPermanent) || !strings.Contains(err.Error(), "private") {
		t.Fatalf("loopback webhook must be refused, got %v", err)
	}
}

func TestPayloadText(t *testing.T) {
	start := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	end := start.Add(42 * time.Minute)
	p := Payload{Type: "event.resolved", SiteName: "HQ", Event: store.Event{Verdict: "upstream", StartedAt: start, ResolvedAt: &end, Summary: "x"}}
	if p.Subject() != "[PathLedger] Resolved: HQ — problem beyond the ISP" || !strings.Contains(p.Text(), "after 42m0s") {
		t.Fatalf("unexpected text: %q / %q", p.Subject(), p.Text())
	}
}
