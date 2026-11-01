// Package alert delivers event notifications by email, Telegram and signed
// webhooks. Notifications are written to a database outbox first and sent
// by a dispatcher, so a slow or failing channel never blocks analysis and
// nothing is lost if the controller restarts.
package alert

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/analytics"
	"github.com/sanjeev864/pathledger/internal/controller/store"
)

// Payload is what every channel receives. It is also the webhook body.
type Payload struct {
	Type      string      `json:"type"` // "event.opened", "event.verdict_changed", "event.resolved" or "test"
	SiteName  string      `json:"site_name"`
	Event     store.Event `json:"event"`
	Link      string      `json:"link,omitempty"`
	CreatedAt time.Time   `json:"created_at"`
}

// Subject is a one-line description for email subjects and chat headers.
func (p Payload) Subject() string {
	if p.Type == "test" {
		return "[PathLedger] Test notification"
	}
	label := verdictLabel(p.Event.Verdict)
	switch p.Type {
	case "event.resolved":
		return fmt.Sprintf("[PathLedger] Resolved: %s — %s", p.SiteName, label)
	case "event.verdict_changed":
		return fmt.Sprintf("[PathLedger] Updated: %s — %s", p.SiteName, label)
	default:
		return fmt.Sprintf("[PathLedger] %s — %s", p.SiteName, label)
	}
}

// Text is the plain-text body used by email and Telegram.
func (p Payload) Text() string {
	if p.Type == "test" {
		return "This is a test notification from PathLedger. If you can read it, this channel works."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", p.Subject())
	if p.Type == "event.resolved" && p.Event.ResolvedAt != nil {
		fmt.Fprintf(&b, "Resolved at %s after %s.\n\n", p.Event.ResolvedAt.UTC().Format("2006-01-02 15:04 MST"),
			p.Event.ResolvedAt.Sub(p.Event.StartedAt).Round(time.Minute))
	} else {
		fmt.Fprintf(&b, "Started %s.\n\n", p.Event.StartedAt.UTC().Format("2006-01-02 15:04 MST"))
	}
	b.WriteString(p.Event.Summary)
	if len(p.Event.Evidence) > 0 {
		b.WriteString("\n\nEvidence:\n")
		for _, e := range p.Event.Evidence {
			fmt.Fprintf(&b, "- %s\n", e)
		}
	}
	if p.Link != "" {
		fmt.Fprintf(&b, "\n%s\n", p.Link)
	}
	return b.String()
}

func verdictLabel(v string) string {
	switch analytics.Verdict(v) {
	case analytics.VerdictLocal:
		return "local network problem"
	case analytics.VerdictISP:
		return "internet provider (ISP) problem"
	case analytics.VerdictUpstream:
		return "problem beyond the ISP"
	case analytics.VerdictDNS:
		return "DNS resolution problem"
	case analytics.VerdictApp:
		return "application or service problem"
	case analytics.VerdictOffline:
		return "site offline"
	default:
		return "network problem"
	}
}

// Sender delivers a payload to one channel.
type Sender interface {
	Send(ctx context.Context, ch store.Channel, p Payload) error
}

// ErrPermanent wraps errors that retrying cannot fix (bad address,
// rejected credentials, channel misconfiguration).
var ErrPermanent = errors.New("permanent delivery failure")

func permanent(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrPermanent, fmt.Sprintf(format, args...))
}

// Store is the persistence the alert package needs.
type Store interface {
	SiteNames(ctx context.Context, ids []string) (map[string]string, error)
	EnqueueDeliveries(ctx context.Context, tenantID, channelID string, payload any) (int, error)
	ClaimDeliveries(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]store.Delivery, error)
	CompleteDelivery(ctx context.Context, id string, now time.Time, sendErr error, retryAt time.Time) error
}

// Notifier implements analytics.Notifier by writing to the outbox.
type Notifier struct {
	Store     Store
	Log       *slog.Logger
	PublicURL string // dashboard base URL for links; optional
}

// Notify enqueues an event change for every enabled channel of its tenant.
func (n *Notifier) Notify(ctx context.Context, c analytics.Change, ev store.Event) {
	names, err := n.Store.SiteNames(ctx, []string{ev.SiteID})
	if err != nil {
		n.Log.Error("loading site name for notification", "error", err)
	}
	p := Payload{Type: "event." + string(c), SiteName: names[ev.SiteID], Event: ev, CreatedAt: time.Now().UTC()}
	if p.SiteName == "" {
		p.SiteName = "site " + ev.SiteID
	}
	if n.PublicURL != "" {
		p.Link = strings.TrimRight(n.PublicURL, "/") + "/#/events/" + ev.ID
	}
	if _, err := n.Store.EnqueueDeliveries(ctx, ev.TenantID, "", p); err != nil {
		n.Log.Error("queueing notifications", "event_id", ev.ID, "error", err)
	}
}
