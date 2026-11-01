package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strings"

	"github.com/sanjeev864/pathledger/internal/controller/store"
)

type channelRequest struct {
	Kind   string   `json:"kind"`
	Name   string   `json:"name"`
	To     []string `json:"to"`      // email
	ChatID string   `json:"chat_id"` // telegram
	URL    string   `json:"url"`     // webhook
}

var chatIDRE = regexp.MustCompile(`^(-?[0-9]{1,20}|@[A-Za-z0-9_]{5,32})$`)

// channelView hides webhook secrets in listings.
func channelView(c store.Channel) store.Channel {
	if _, ok := c.Config["secret"]; ok {
		cfg := map[string]any{}
		for k, v := range c.Config {
			cfg[k] = v
		}
		cfg["secret"] = "********"
		c.Config = cfg
	}
	return c
}

func (s *Server) createChannel(w http.ResponseWriter, r *http.Request) {
	var req channelRequest
	if !decodeJSON(w, r, r.Body, &req, maxSmallBody) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 200 {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "name: must be 1 to 200 characters")
		return
	}
	ch := store.Channel{TenantID: tenantID(r), Kind: req.Kind, Name: req.Name}
	var secret string
	switch req.Kind {
	case "email":
		if len(req.To) == 0 || len(req.To) > 10 {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed", "to: between 1 and 10 addresses")
			return
		}
		to := make([]any, 0, len(req.To))
		for _, a := range req.To {
			addr, err := mail.ParseAddress(a)
			if err != nil {
				writeError(w, http.StatusUnprocessableEntity, "validation_failed", "to: invalid address "+a)
				return
			}
			to = append(to, addr.Address)
		}
		ch.Config = map[string]any{"to": to}
	case "telegram":
		if !chatIDRE.MatchString(req.ChatID) {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed", "chat_id: a numeric chat ID or @channelname")
			return
		}
		ch.Config = map[string]any{"chat_id": req.ChatID}
	case "webhook":
		u, err := url.Parse(req.URL)
		if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && !(s.allowHTTPWebhooks && u.Scheme == "http")) || len(req.URL) > 1000 {
			writeError(w, http.StatusUnprocessableEntity, "validation_failed", "url: must be an https:// URL without credentials")
			return
		}
		b := make([]byte, 32)
		_, _ = rand.Read(b)
		secret = hex.EncodeToString(b)
		ch.Config = map[string]any{"url": req.URL, "secret": secret}
	default:
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "kind: must be email, telegram or webhook")
		return
	}
	created, err := s.store.CreateChannel(r.Context(), ch)
	if err != nil {
		s.storeError(w, r, err, "channel")
		return
	}
	// The webhook signing secret is shown once, at creation.
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	chs, err := s.store.ListChannels(r.Context(), tenantID(r))
	if err != nil {
		s.storeError(w, r, err, "channels")
		return
	}
	for i := range chs {
		chs[i] = channelView(chs[i])
	}
	writeJSON(w, http.StatusOK, newList(chs))
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "channel")
	if !ok {
		return
	}
	if err := s.store.DeleteChannel(r.Context(), tenantID(r), id); err != nil {
		s.storeError(w, r, err, "channel")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testChannel(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "channel")
	if !ok {
		return
	}
	if _, err := s.store.GetChannel(r.Context(), tenantID(r), id); err != nil {
		s.storeError(w, r, err, "channel")
		return
	}
	n, err := s.store.EnqueueDeliveries(r.Context(), tenantID(r), id, map[string]any{"type": "test", "created_at": s.now().UTC()})
	if err != nil {
		s.storeError(w, r, err, "channel")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"queued": n})
}

func (s *Server) listDeliveries(w http.ResponseWriter, r *http.Request) {
	ds, err := s.store.ListDeliveries(r.Context(), tenantID(r), 100)
	if err != nil {
		s.storeError(w, r, err, "deliveries")
		return
	}
	writeJSON(w, http.StatusOK, newList(ds))
}
