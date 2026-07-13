package web

import (
	"net/http"
	"strings"

	"github.com/floreabogdan/birdy/internal/store"
)

type bmpView struct {
	Active   string
	ReadOnly bool
	Stations []store.BMPStation
	// Live indexes the running BIRD protocols so a station's session state shows
	// up once a config carrying it has been applied.
	Live  map[string]protoRow
	Pager Pager
	Flash string
}

type bmpFormView struct {
	Active   string
	ReadOnly bool
	IsNew    bool
	Station  store.BMPStation
	Errs     map[string]string
}

func (s *Server) handleBMPPage(w http.ResponseWriter, r *http.Request) {
	stations, err := s.store.ListBMPStations()
	if err != nil {
		s.serverError(w, "list BMP stations", err)
		return
	}
	offset, limit := parsePageParams(r)
	page := pageSlice(stations, offset, limit)
	render(w, s.log, "bmp.html", bmpView{
		Active: "bmp", ReadOnly: s.readOnly, Stations: page,
		Live: s.liveStates(), Pager: pagerFor(r, offset, limit, len(page), len(stations)),
		Flash: r.URL.Query().Get("flash"),
	})
}

func (s *Server) handleBMPNew(w http.ResponseWriter, r *http.Request) {
	// Defaults matching the common case: the well-known BMP port, both RIB views
	// on, and BIRD's default send buffer.
	st := store.BMPStation{Port: 1790, Enabled: true, PrePolicy: true, PostPolicy: true}
	render(w, s.log, "bmp_form.html", bmpFormView{Active: "bmp", ReadOnly: s.readOnly, IsNew: true, Station: st})
}

func (s *Server) handleBMPEdit(w http.ResponseWriter, r *http.Request) {
	st, ok := namedEntity(s, w, r, s.store.GetBMPStationByName, "BMP station")
	if !ok {
		return
	}
	render(w, s.log, "bmp_form.html", bmpFormView{Active: "bmp", ReadOnly: s.readOnly, Station: st})
}

func bmpFromForm(r *http.Request) store.BMPStation {
	return store.BMPStation{
		Name:          r.FormValue("name"),
		Description:   strings.TrimSpace(r.FormValue("description")),
		Address:       r.FormValue("address"),
		Port:          formInt(r, "port"),
		Enabled:       r.FormValue("enabled") == "on",
		PrePolicy:     r.FormValue("monitorPre") == "on",
		PostPolicy:    r.FormValue("monitorPost") == "on",
		TxBufferLimit: formInt(r, "txBufferLimit"),
	}
}

func (s *Server) handleBMPSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	isNew := r.PathValue("name") == ""
	st := bmpFromForm(r)

	if !isNew {
		existing, ok := namedEntity(s, w, r, s.store.GetBMPStationByName, "BMP station")
		if !ok {
			return
		}
		st.ID = existing.ID
	}

	errs := st.Validate()
	if len(errs) == 0 {
		var err error
		if isNew {
			_, err = s.store.CreateBMPStation(st)
		} else {
			err = s.store.UpdateBMPStation(st)
		}
		if err != nil {
			if isUniqueViolation(err) {
				errs["name"] = "A BMP station with this name already exists."
			} else {
				s.serverError(w, "save BMP station", err)
				return
			}
		}
	}
	if len(errs) == 0 {
		http.Redirect(w, r, "/bmp?flash="+flash("Saved "+st.Name), http.StatusSeeOther)
		return
	}
	render(w, s.log, "bmp_form.html", bmpFormView{Active: "bmp", ReadOnly: s.readOnly, IsNew: isNew, Station: st, Errs: errs})
}

func (s *Server) handleBMPDelete(w http.ResponseWriter, r *http.Request) {
	st, ok := namedEntity(s, w, r, s.store.GetBMPStationByName, "BMP station")
	if !ok {
		return
	}
	if err := s.store.DeleteBMPStation(st.ID); err != nil {
		s.serverError(w, "delete BMP station", err)
		return
	}
	http.Redirect(w, r, "/bmp?flash="+flash("Deleted "+st.Name), http.StatusSeeOther)
}
