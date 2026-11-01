package api

import (
	"net/http"
	"strconv"

	"github.com/sanjeev864/pathledger/internal/controller/store"
)

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.EventFilter{State: q.Get("state"), SiteID: q.Get("site_id"), Limit: 100}
	if f.State != "" && f.State != store.EventOpen && f.State != store.EventResolved {
		writeError(w, http.StatusBadRequest, "invalid_query", "state: must be open or resolved")
		return
	}
	if f.SiteID != "" && !validID(f.SiteID) {
		writeError(w, http.StatusBadRequest, "invalid_query", "site_id: must be an ID")
		return
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, http.StatusBadRequest, "invalid_query", "limit: must be between 1 and 1000")
			return
		}
		f.Limit = n
	}
	events, err := s.store.ListEvents(r.Context(), tenantID(r), f)
	if err != nil {
		s.storeError(w, r, err, "events")
		return
	}
	writeJSON(w, http.StatusOK, newList(events))
}

func (s *Server) getEvent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "event")
	if !ok {
		return
	}
	ev, err := s.store.GetEvent(r.Context(), tenantID(r), id)
	if err != nil {
		s.storeError(w, r, err, "event")
		return
	}
	writeJSON(w, http.StatusOK, ev)
}
