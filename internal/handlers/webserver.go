package handlers

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	"aura/internal/webserver"

	"github.com/gorilla/websocket"
)

type webServerIndexData struct {
	Sites        []webserver.Site
	NginxMissing bool
	Flash        string
	FlashType    string
}

type webServerCreateData struct {
	Input      webserver.Input
	Errors     map[string]string
	TestOutput string
}

type webServerEditData struct {
	Site       webserver.Site
	Config     string
	TestOutput string
	SSLError   string
	Email      string
}

var webServerFlashes = map[string]string{
	"created":    "Site created.",
	"updated":    "Site updated.",
	"deleted":    "Site deleted.",
	"enabled":    "Site enabled.",
	"disabled":   "Site disabled.",
	"ssl_issued": "SSL certificate issued.",
	"ssl_failed": "Site created, but issuing the SSL certificate failed. Check the server logs.",
	"failed":     "Site action failed. Check the server logs.",
}

func (h *Handlers) WebServerIndex(w http.ResponseWriter, r *http.Request) {
	data := webServerIndexData{}
	list, err := webserver.List()
	if errors.Is(err, webserver.ErrNginxMissing) {
		data.NginxMissing = true
	} else if err != nil {
		serverError(w, "list sites", err)
		return
	}
	data.Sites = list
	status := r.URL.Query().Get("status")
	data.Flash = webServerFlashes[status]
	switch status {
	case "failed":
		data.FlashType = "danger"
	case "ssl_failed":
		data.FlashType = "warning"
	default:
		data.FlashType = "success"
	}
	h.render(w, http.StatusOK, "webserver/index.html", page{
		Title:  "Web Server",
		Active: "webserver",
		Data:   data,
	})
}

func (h *Handlers) WebServerCreate(w http.ResponseWriter, r *http.Request) {
	h.renderWebServerCreate(w, http.StatusOK, webServerCreateData{Input: webserver.Input{Port: 80, WebSocket: true}})
}

func (h *Handlers) WebServerStore(w http.ResponseWriter, r *http.Request) {
	in := webServerInputFromForm(r)
	if errs := in.Validate(); len(errs) > 0 {
		h.renderWebServerCreate(w, http.StatusUnprocessableEntity, webServerCreateData{Input: in, Errors: errs})
		return
	}
	if err := webserver.Create(in); err != nil {
		log.Printf("create site %s: %v", in.Name, err)
		h.renderWebServerCreate(w, http.StatusUnprocessableEntity, webServerCreateData{Input: in, TestOutput: webServerErrorOutput(err)})
		return
	}
	if in.SSL {
		if err := webserver.IssueSSL(in.DomainList(), in.Email); err != nil {
			log.Printf("issue ssl for site %s: %v", in.Name, err)
			http.Redirect(w, r, "/web-server?status=ssl_failed", http.StatusSeeOther)
			return
		}
	}
	http.Redirect(w, r, "/web-server?status=created", http.StatusSeeOther)
}

func (h *Handlers) WebServerEdit(w http.ResponseWriter, r *http.Request) {
	site, ok := h.findSite(w, r)
	if !ok {
		return
	}
	h.renderWebServerEdit(w, http.StatusOK, webServerEditData{Site: site, Config: site.Config})
}

func (h *Handlers) WebServerUpdate(w http.ResponseWriter, r *http.Request) {
	site, ok := h.findSite(w, r)
	if !ok {
		return
	}
	config := strings.ReplaceAll(r.PostFormValue("config"), "\r\n", "\n")
	if !strings.HasSuffix(config, "\n") {
		config += "\n"
	}
	err := webserver.Save(site.Name, config)
	if errors.Is(err, webserver.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("save site %s: %v", site.Name, err)
		h.renderWebServerEdit(w, http.StatusUnprocessableEntity, webServerEditData{Site: site, Config: config, TestOutput: webServerErrorOutput(err)})
		return
	}
	http.Redirect(w, r, "/web-server?status=updated", http.StatusSeeOther)
}

func (h *Handlers) WebServerSSL(w http.ResponseWriter, r *http.Request) {
	site, ok := h.findSite(w, r)
	if !ok {
		return
	}
	email := strings.TrimSpace(r.PostFormValue("email"))
	data := webServerEditData{Site: site, Config: site.Config, Email: email}
	switch {
	case email != "" && !webserver.ValidEmail(email):
		data.SSLError = "Enter a valid email address."
	case !site.Enabled:
		data.SSLError = "Enable the site before issuing an SSL certificate."
	case len(site.Domains) == 0:
		data.SSLError = "No domains found in the server_name of this site."
	}
	if data.SSLError != "" {
		h.renderWebServerEdit(w, http.StatusUnprocessableEntity, data)
		return
	}
	if err := webserver.IssueSSL(site.Domains, email); err != nil {
		log.Printf("issue ssl for site %s: %v", site.Name, err)
		data.SSLError = err.Error()
		h.renderWebServerEdit(w, http.StatusUnprocessableEntity, data)
		return
	}
	http.Redirect(w, r, "/web-server?status=ssl_issued", http.StatusSeeOther)
}

func (h *Handlers) WebServerLogs(w http.ResponseWriter, r *http.Request) {
	site, ok := h.findSite(w, r)
	if !ok {
		return
	}
	logPath, own := site.LogPath(r.URL.Query().Get("type"))
	if logPath == "" {
		http.NotFound(w, r)
		return
	}
	conn, err := shellUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("site logs upgrade %s: %v", site.Name, err)
		return
	}
	defer conn.Close()
	header := "\x1b[90m==> " + logPath
	if !own {
		header += " (shared nginx log, this site has no own log file)"
	}
	conn.WriteMessage(websocket.BinaryMessage, []byte(header+" <==\x1b[0m\r\n"))
	cmd := webserver.TailLog(logPath, 1000)
	out := &wsWriter{conn: conn}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		log.Printf("site logs start %s: %v", site.Name, err)
		conn.WriteMessage(websocket.BinaryMessage, []byte("Failed to read logs: "+err.Error()+"\r\n"))
		return
	}
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cmd.Process.Signal(syscall.SIGTERM)
				return
			}
		}
	}()
	cmd.Wait()
	conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
}

func (h *Handlers) WebServerEnable(w http.ResponseWriter, r *http.Request) {
	h.webServerAction(w, r, "enable", webserver.Enable, "enabled")
}

func (h *Handlers) WebServerDisable(w http.ResponseWriter, r *http.Request) {
	h.webServerAction(w, r, "disable", webserver.Disable, "disabled")
}

func (h *Handlers) WebServerDelete(w http.ResponseWriter, r *http.Request) {
	h.webServerAction(w, r, "delete", webserver.Delete, "deleted")
}

func (h *Handlers) webServerAction(w http.ResponseWriter, r *http.Request, action string, fn func(string) error, status string) {
	name := r.PathValue("name")
	if !webserver.ValidName(name) {
		http.NotFound(w, r)
		return
	}
	err := fn(name)
	if errors.Is(err, webserver.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("%s site %s: %v", action, name, err)
		http.Redirect(w, r, "/web-server?status=failed", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/web-server?status="+status, http.StatusSeeOther)
}

func (h *Handlers) renderWebServerCreate(w http.ResponseWriter, status int, data webServerCreateData) {
	h.render(w, status, "webserver/create.html", page{
		Title:  "Create site",
		Active: "webserver",
		Data:   data,
	})
}

func (h *Handlers) renderWebServerEdit(w http.ResponseWriter, status int, data webServerEditData) {
	h.render(w, status, "webserver/edit.html", page{
		Title:  "Edit " + data.Site.Name,
		Active: "webserver",
		Data:   data,
	})
}

func (h *Handlers) findSite(w http.ResponseWriter, r *http.Request) (webserver.Site, bool) {
	site, err := webserver.Get(r.PathValue("name"))
	if errors.Is(err, webserver.ErrNotFound) {
		http.NotFound(w, r)
		return webserver.Site{}, false
	}
	if err != nil {
		serverError(w, "get site", err)
		return webserver.Site{}, false
	}
	return site, true
}

func webServerInputFromForm(r *http.Request) webserver.Input {
	in := webserver.Input{
		Name:      r.PostFormValue("name"),
		Domains:   r.PostFormValue("domains"),
		Upstream:  r.PostFormValue("upstream"),
		WebSocket: r.PostFormValue("websocket") == "1",
		SSL:       r.PostFormValue("ssl") == "1",
		Email:     r.PostFormValue("email"),
	}
	port := strings.TrimSpace(r.PostFormValue("port"))
	if port == "" {
		in.Port = 80
	} else {
		in.Port, _ = strconv.Atoi(port)
	}
	in.Normalize()
	return in
}

func webServerErrorOutput(err error) string {
	var testErr *webserver.TestError
	if errors.As(err, &testErr) {
		return testErr.Output
	}
	return err.Error()
}
