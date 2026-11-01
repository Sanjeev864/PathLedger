package api

import (
	"net/http"
	"time"

	"github.com/sanjeev864/pathledger/internal/controller/report"
)

// maxReportIncidents matches the store's cap so truncation can be reported.
const maxReportIncidents = 2000

// slaReport returns the monthly service-level report for the tenant.
// Query: month=YYYY-MM (default: the current month), tz=IANA zone (default UTC).
func (s *Server) slaReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	loc := time.UTC
	if v := q.Get("tz"); v != "" {
		l, err := time.LoadLocation(v)
		if err != nil || len(v) > 64 {
			writeError(w, http.StatusBadRequest, "invalid_query", "tz: an IANA time zone such as Asia/Kolkata")
			return
		}
		loc = l
	}
	now := s.now()
	month := now.In(loc)
	if v := q.Get("month"); v != "" {
		m, err := time.ParseInLocation("2006-01", v, loc)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_query", "month: YYYY-MM")
			return
		}
		month = m
	}
	from, to := report.MonthBounds(month.Year(), month.Month(), loc)
	if !from.Before(now) {
		writeError(w, http.StatusBadRequest, "invalid_query", "month: must not be in the future")
		return
	}
	if from.Before(now.AddDate(-5, 0, 0)) {
		writeError(w, http.StatusBadRequest, "invalid_query", "month: at most five years ago")
		return
	}
	data, err := s.store.SLAReportData(r.Context(), tenantID(r), from, to)
	if err != nil {
		s.storeError(w, r, err, "report")
		return
	}
	writeJSON(w, http.StatusOK, report.Build(data, from.Format("2006-01"), loc, from, to, now, maxReportIncidents))
}
