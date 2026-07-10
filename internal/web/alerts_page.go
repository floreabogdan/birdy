package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/floreabogdan/birdy/internal/notify"
	"github.com/floreabogdan/birdy/internal/store"
)

type alertsView struct {
	Active       string
	ReadOnly     bool
	Flash        string
	Err          string
	Destinations []store.Destination
}

type alertFormView struct {
	Active   string
	ReadOnly bool
	IsNew    bool
	Dest     store.Destination
	Errs     map[string]string
	// HasPassword reports whether a stored SMTP password exists, so the edit
	// form can show "unchanged" without ever rendering the secret.
	HasPassword bool
}

func (s *Server) handleAlertsList(w http.ResponseWriter, r *http.Request) {
	dests, err := s.store.ListAlertDestinations()
	if err != nil {
		s.serverError(w, "list alert destinations", err)
		return
	}
	render(w, s.log, "alerts.html", alertsView{
		Active: "alerts", ReadOnly: s.readOnly, Destinations: dests,
		Flash: r.URL.Query().Get("flash"), Err: r.URL.Query().Get("err"),
	})
}

func (s *Server) handleAlertNew(w http.ResponseWriter, r *http.Request) {
	s.renderAlertForm(w, alertFormView{
		Active: "alerts", ReadOnly: s.readOnly, IsNew: true,
		Dest: store.Destination{Type: store.AlertSlack, Enabled: true, SMTPPort: 587, SMTPSecurity: store.SMTPStartTLS},
	})
}

func (s *Server) handleAlertEdit(w http.ResponseWriter, r *http.Request) {
	d, err := s.alertFromPath(w, r)
	if err != nil {
		return
	}
	s.renderAlertForm(w, alertFormView{
		Active: "alerts", ReadOnly: s.readOnly, Dest: d, HasPassword: d.SMTPPassword != "",
	})
}

func (s *Server) handleAlertSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	isNew := r.PathValue("id") == ""
	d := alertFromForm(r)

	if !isNew {
		existing, err := s.alertFromPath(w, r)
		if err != nil {
			return
		}
		d.ID = existing.ID
		// A blank SMTP password means "leave it alone", like the peer password.
		if d.SMTPPassword == "" {
			d.SMTPPassword = existing.SMTPPassword
		}
	}

	errs := d.Validate()
	if len(errs) > 0 {
		s.renderAlertForm(w, alertFormView{
			Active: "alerts", ReadOnly: s.readOnly, IsNew: isNew, Dest: d, Errs: errs,
			HasPassword: !isNew && d.SMTPPassword != "",
		})
		return
	}

	var err error
	if isNew {
		_, err = s.store.CreateAlertDestination(d)
	} else {
		err = s.store.UpdateAlertDestination(d)
	}
	if isUniqueViolation(err) {
		errs["name"] = "A destination with this name already exists."
		s.renderAlertForm(w, alertFormView{Active: "alerts", ReadOnly: s.readOnly, IsNew: isNew, Dest: d, Errs: errs})
		return
	}
	if err != nil {
		s.serverError(w, "save alert destination", err)
		return
	}
	http.Redirect(w, r, "/alerts?flash="+flash("Saved "+d.Name), http.StatusSeeOther)
}

func (s *Server) handleAlertDelete(w http.ResponseWriter, r *http.Request) {
	d, err := s.alertFromPath(w, r)
	if err != nil {
		return
	}
	if err := s.store.DeleteAlertDestination(d.ID); err != nil {
		s.serverError(w, "delete alert destination", err)
		return
	}
	http.Redirect(w, r, "/alerts?flash="+flash("Deleted "+d.Name), http.StatusSeeOther)
}

// handleAlertTest sends a synthetic alert to one destination and reports the
// result. It uses the stored config, so save before testing.
func (s *Server) handleAlertTest(w http.ResponseWriter, r *http.Request) {
	d, err := s.alertFromPath(w, r)
	if err != nil {
		return
	}
	if terr := notify.NewDispatcher(s.store, s.log).SendTest(d); terr != nil {
		http.Redirect(w, r, "/alerts?err="+flash("Test to "+d.Name+" failed: "+terr.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/alerts?flash="+flash("Test alert sent to "+d.Name), http.StatusSeeOther)
}

func alertFromForm(r *http.Request) store.Destination {
	port, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("smtpPort")))
	return store.Destination{
		Name:         strings.TrimSpace(r.FormValue("name")),
		Type:         r.FormValue("type"),
		Enabled:      r.FormValue("enabled") == "on",
		URL:          strings.TrimSpace(r.FormValue("url")),
		SMTPHost:     strings.TrimSpace(r.FormValue("smtpHost")),
		SMTPPort:     port,
		SMTPUsername: strings.TrimSpace(r.FormValue("smtpUsername")),
		SMTPPassword: r.FormValue("smtpPassword"),
		SMTPFrom:     strings.TrimSpace(r.FormValue("smtpFrom")),
		SMTPTo:       r.FormValue("smtpTo"),
		SMTPSecurity: r.FormValue("smtpSecurity"),
	}
}

func (s *Server) alertFromPath(w http.ResponseWriter, r *http.Request) (store.Destination, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return store.Destination{}, err
	}
	d, err := s.store.GetAlertDestination(id)
	if err == store.ErrNotFound {
		http.NotFound(w, r)
		return store.Destination{}, err
	}
	if err != nil {
		s.serverError(w, "get alert destination", err)
		return store.Destination{}, err
	}
	return d, nil
}

func (s *Server) renderAlertForm(w http.ResponseWriter, v alertFormView) {
	render(w, s.log, "alert_form.html", v)
}
