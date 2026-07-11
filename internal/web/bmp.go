package web

import (
	"net/http"
	"strconv"
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
	render(w, s.log, "bmp.html", bmpView{
		Active: "bmp", ReadOnly: s.readOnly, Stations: stations,
		Live: s.liveStates(), Flash: r.URL.Query().Get("flash"),
	})
}

func (s *Server) handleBMPNew(w http.ResponseWriter, r *http.Request) {
	// Defaults matching the common case: the well-known BMP port, both RIB views
	// on, and BIRD's default send buffer.
	st := store.BMPStation{Port: 1790, Enabled: true, PrePolicy: true, PostPolicy: true}
	render(w, s.log, "bmp_form.html", bmpFormView{Active: "bmp", ReadOnly: s.readOnly, IsNew: true, Station: st})
}

func (s *Server) handleBMPEdit(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.GetBMPStationByName(r.PathValue("name"))
	if err == store.ErrNotFound {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, "get BMP station", err)
		return
	}
	render(w, s.log, "bmp_form.html", bmpFormView{Active: "bmp", ReadOnly: s.readOnly, Station: st})
}

func bmpFromForm(r *http.Request) store.BMPStation {
	atoi := func(k string) int {
		n, _ := strconv.Atoi(strings.TrimSpace(r.FormValue(k)))
		return n
	}
	return store.BMPStation{
		Name:          r.FormValue("name"),
		Description:   strings.TrimSpace(r.FormValue("description")),
		Address:       r.FormValue("address"),
		Port:          atoi("port"),
		Enabled:       r.FormValue("enabled") == "on",
		PrePolicy:     r.FormValue("monitorPre") == "on",
		PostPolicy:    r.FormValue("monitorPost") == "on",
		TxBufferLimit: atoi("txBufferLimit"),
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
		existing, err := s.store.GetBMPStationByName(r.PathValue("name"))
		if err == store.ErrNotFound {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			s.serverError(w, "get BMP station", err)
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
	name := r.PathValue("name")
	st, err := s.store.GetBMPStationByName(name)
	if err == store.ErrNotFound {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, "get BMP station", err)
		return
	}
	if err := s.store.DeleteBMPStation(st.ID); err != nil {
		s.serverError(w, "delete BMP station", err)
		return
	}
	http.Redirect(w, r, "/bmp?flash="+flash("Deleted "+name), http.StatusSeeOther)
}
