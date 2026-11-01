package alert

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"net/smtp"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/store"
	"github.com/sanjeev864/pathledger/internal/enrich"
)

// Router picks the sender for a channel kind.
type Router map[string]Sender

// Send implements Sender.
func (r Router) Send(ctx context.Context, ch store.Channel, p Payload) error {
	s, ok := r[ch.Kind]
	if !ok {
		return permanent("%s notifications are not configured on this controller", ch.Kind)
	}
	return s.Send(ctx, ch, p)
}

func configString(ch store.Channel, key string) string {
	v, _ := ch.Config[key].(string)
	return v
}

// ---- Email ----

// SMTPConfig configures outgoing email. Port 465 uses implicit TLS; other
// ports use STARTTLS when the server offers it.
type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

// Email sends notifications over SMTP.
type Email struct {
	Config SMTPConfig
}

// Send implements Sender.
func (e *Email) Send(ctx context.Context, ch store.Channel, p Payload) error {
	to, err := emailRecipients(ch)
	if err != nil {
		return err
	}
	from, err := mail.ParseAddress(e.Config.From)
	if err != nil {
		return permanent("invalid sender address %q", e.Config.From)
	}
	var msg bytes.Buffer
	fmt.Fprintf(&msg, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n",
		from.String(), strings.Join(to, ", "), sanitizeHeader(p.Subject()), time.Now().UTC().Format(time.RFC1123Z))
	msg.WriteString(strings.ReplaceAll(p.Text(), "\n", "\r\n"))

	addr := net.JoinHostPort(e.Config.Host, strconv.Itoa(e.Config.Port))
	var auth smtp.Auth
	if e.Config.Username != "" {
		auth = smtp.PlainAuth("", e.Config.Username, e.Config.Password, e.Config.Host)
	}
	done := make(chan error, 1)
	go func() {
		if e.Config.Port == 465 {
			done <- sendImplicitTLS(addr, e.Config.Host, auth, from.Address, to, msg.Bytes())
			return
		}
		done <- smtp.SendMail(addr, auth, from.Address, to, msg.Bytes())
	}()
	select {
	case err := <-done:
		return classifySMTP(err)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// classifySMTP treats 5xx replies as permanent and everything else
// (network errors, 4xx "try later") as retryable.
func classifySMTP(err error) error {
	var tp *textproto.Error
	if errors.As(err, &tp) && tp.Code >= 500 {
		return fmt.Errorf("%w: %v", ErrPermanent, err)
	}
	return err
}

func sendImplicitTLS(addr, host string, auth smtp.Auth, from string, to []string, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return err
	}
	defer c.Close()
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, r := range to {
		if err := c.Rcpt(r); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func emailRecipients(ch store.Channel) ([]string, error) {
	raw, _ := ch.Config["to"].([]any)
	var out []string
	for _, v := range raw {
		s, _ := v.(string)
		a, err := mail.ParseAddress(s)
		if err != nil {
			return nil, permanent("invalid recipient %q", s)
		}
		out = append(out, a.Address)
	}
	if len(out) == 0 {
		return nil, permanent("email channel has no recipients")
	}
	return out, nil
}

func sanitizeHeader(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

// ---- Telegram ----

// Telegram sends messages through a bot owned by the PathLedger operator;
// each channel supplies the chat ID to post to.
type Telegram struct {
	Token   string
	BaseURL string // defaults to https://api.telegram.org
	Client  *http.Client
}

// Send implements Sender.
func (t *Telegram) Send(ctx context.Context, ch store.Channel, p Payload) error {
	chat := configString(ch, "chat_id")
	if chat == "" {
		return permanent("telegram channel has no chat_id")
	}
	base := t.BaseURL
	if base == "" {
		base = "https://api.telegram.org"
	}
	body, _ := json.Marshal(map[string]any{"chat_id": chat, "text": p.Text(), "disable_web_page_preview": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+t.Token+"/sendMessage", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.Client.Do(req)
	if err != nil {
		// Never include the URL: it contains the bot token.
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("telegram request failed: %w", ue.Err)
		}
		return fmt.Errorf("telegram request failed: %w", err)
	}
	defer resp.Body.Close()
	return httpOutcome("telegram", resp)
}

// ---- Webhook ----

// Webhook POSTs the JSON payload, signed with HMAC-SHA256 over the body in
// the X-PathLedger-Signature header ("sha256=<hex>").
type Webhook struct {
	Client *http.Client
}

// Send implements Sender.
func (w *Webhook) Send(ctx context.Context, ch store.Channel, p Payload) error {
	target := configString(ch, "url")
	secret := configString(ch, "secret")
	if target == "" || secret == "" {
		return permanent("webhook channel needs url and secret")
	}
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return permanent("invalid webhook URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "PathLedger-Webhook")
	req.Header.Set("X-PathLedger-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("X-PathLedger-Event", p.Type)
	resp, err := w.Client.Do(req)
	if err != nil {
		if errors.Is(err, errBlockedAddress) {
			return permanent("%v", err)
		}
		return err
	}
	defer resp.Body.Close()
	return httpOutcome("webhook", resp)
}

func httpOutcome(what string, resp *http.Response) error {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return fmt.Errorf("%s returned %d", what, resp.StatusCode)
	default:
		return permanent("%s returned %d", what, resp.StatusCode)
	}
}

var errBlockedAddress = errors.New("webhook target resolves to a private or reserved address")

// SafeHTTPClient refuses to connect to private, loopback and other
// non-public addresses, so customer-supplied webhook URLs cannot be used to
// probe the controller's internal network (SSRF). The check runs on the
// resolved address at connect time, which also defeats DNS rebinding.
func SafeHTTPClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			if allowPrivate {
				return nil
			}
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			ip, err := netip.ParseAddr(host)
			if err != nil || enrich.ScopeOf(ip) != enrich.ScopePublic {
				return errBlockedAddress
			}
			return nil
		},
	}
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			MaxIdleConns:        10,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}
