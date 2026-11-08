package handlers

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"syscall"
	"time"

	"aura/internal/proxy"

	"github.com/gorilla/websocket"
)

type proxyIndexData struct {
	Sites        []proxy.Site
	NginxMissing bool
	Flash        string
	FlashType    string
}

type proxyCreateData struct {
	Input      proxy.Input
	Errors     map[string]string
	TestOutput string
}

type proxyEditData struct {
	Site       proxy.Site
	Config     string
	TestOutput string
	SSLError   string
	Email      string
}

var proxyFlashes = map[string]string{
	"created":    "Site created.",
	"updated":    "Site updated.",
	"deleted":    "Site deleted.",
	"enabled":    "Site enabled.",
	"disabled":   "Site disabled.",
	"ssl_issued": "SSL certificate issued.",
	"ssl_failed": "Site created, but issuing the SSL certificate failed. Check the server logs.",
	"failed":     "Site action failed. Check the server logs.",
}

func (h *Handlers) ProxyIndex(w http.ResponseWriter, r *http.Request) {
	data := proxyIndexData{}
	list, err := proxy.List()
	if errors.Is(err, proxy.ErrNginxMissing) {
		data.NginxMissing = true
	} else if err != nil {
		serverError(w, "list sites", err)
		return
	}
	data.Sites = list
	status := r.URL.Query().Get("status")
	data.Flash = proxyFlashes[status]
	switch status {
	case "failed":
		data.FlashType = "danger"
	case "ssl_failed":
		data.FlashType = "warning"
	default:
		data.FlashType = "success"
	}
	h.render(w, http.StatusOK, "proxy/index.html", page{
		Title:  "Reverse Proxy",
		Active: "proxy",
		Data:   data,
	})
}

func (h *Handlers) ProxyCreate(w http.ResponseWriter, r *http.Request) {
	h.renderProxyCreate(w, http.StatusOK, proxyCreateData{Input: proxy.Input{Port: 80, WebSocket: true}})
}

func (h *Handlers) ProxyStore(w http.ResponseWriter, r *http.Request) {
	in := proxyInputFromForm(r)
	if errs := in.Validate(); len(errs) > 0 {
		h.renderProxyCreate(w, http.StatusUnprocessableEntity, proxyCreateData{Input: in, Errors: errs})
		return
	}
	if err := proxy.Create(in); err != nil {
		log.Printf("create site %s: %v", in.Name, err)
		h.renderProxyCreate(w, http.StatusUnprocessableEntity, proxyCreateData{Input: in, TestOutput: proxyErrorOutput(err)})
		return
	}
	if in.SSL {
		if err := proxy.IssueSSL(in.DomainList(), in.Email); err != nil {
			log.Printf("issue ssl for site %s: %v", in.Name, err)
			http.Redirect(w, r, "/proxy?status=ssl_failed", http.StatusSeeOther)
			return
		}
	}
	http.Redirect(w, r, "/proxy?status=created", http.StatusSeeOther)
}

func (h *Handlers) ProxyEdit(w http.ResponseWriter, r *http.Request) {
	site, ok := h.findSite(w, r)
	if !ok {
		return
	}
	h.renderProxyEdit(w, http.StatusOK, proxyEditData{Site: site, Config: site.Config})
}

func (h *Handlers) ProxyUpdate(w http.ResponseWriter, r *http.Request) {
	site, ok := h.findSite(w, r)
	if !ok {
		return
	}
	config := strings.ReplaceAll(r.PostFormValue("config"), "\r\n", "\n")
	if !strings.HasSuffix(config, "\n") {
		config += "\n"
	}
	err := proxy.Save(site.Name, config)
	if errors.Is(err, proxy.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("save site %s: %v", site.Name, err)
		h.renderProxyEdit(w, http.StatusUnprocessableEntity, proxyEditData{Site: site, Config: config, TestOutput: proxyErrorOutput(err)})
		return
	}
	http.Redirect(w, r, "/proxy?status=updated", http.StatusSeeOther)
}

func (h *Handlers) ProxySSL(w http.ResponseWriter, r *http.Request) {
	site, ok := h.findSite(w, r)
	if !ok {
		return
	}
	email := strings.TrimSpace(r.PostFormValue("email"))
	data := proxyEditData{Site: site, Config: site.Config, Email: email}
	switch {
	case email != "" && !proxy.ValidEmail(email):
		data.SSLError = "Enter a valid email address."
	case !site.Enabled:
		data.SSLError = "Enable the site before issuing an SSL certificate."
	case len(site.Domains) == 0:
		data.SSLError = "No domains found in the server_name of this site."
	}
	if data.SSLError != "" {
		h.renderProxyEdit(w, http.StatusUnprocessableEntity, data)
		return
	}
	if err := proxy.IssueSSL(site.Domains, email); err != nil {
		log.Printf("issue ssl for site %s: %v", site.Name, err)
		data.SSLError = err.Error()
		h.renderProxyEdit(w, http.StatusUnprocessableEntity, data)
		return
	}
	http.Redirect(w, r, "/proxy?status=ssl_issued", http.StatusSeeOther)
}

func (h *Handlers) ProxyLogs(w http.ResponseWriter, r *http.Request) {
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
	cmd := proxy.TailLog(logPath, 1000)
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

func (h *Handlers) ProxyEnable(w http.ResponseWriter, r *http.Request) {
	h.proxyAction(w, r, "enable", proxy.Enable, "enabled")
}

func (h *Handlers) ProxyDisable(w http.ResponseWriter, r *http.Request) {
	h.proxyAction(w, r, "disable", proxy.Disable, "disabled")
}

func (h *Handlers) ProxyDelete(w http.ResponseWriter, r *http.Request) {
	h.proxyAction(w, r, "delete", proxy.Delete, "deleted")
}

func (h *Handlers) proxyAction(w http.ResponseWriter, r *http.Request, action string, fn func(string) error, status string) {
	name := r.PathValue("name")
	if !proxy.ValidName(name) {
		http.NotFound(w, r)
		return
	}
	err := fn(name)
	if errors.Is(err, proxy.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		log.Printf("%s site %s: %v", action, name, err)
		http.Redirect(w, r, "/proxy?status=failed", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/proxy?status="+status, http.StatusSeeOther)
}

func (h *Handlers) renderProxyCreate(w http.ResponseWriter, status int, data proxyCreateData) {
	h.render(w, status, "proxy/create.html", page{
		Title:  "Create site",
		Active: "proxy",
		Data:   data,
	})
}

func (h *Handlers) renderProxyEdit(w http.ResponseWriter, status int, data proxyEditData) {
	h.render(w, status, "proxy/edit.html", page{
		Title:  "Edit " + data.Site.Name,
		Active: "proxy",
		Data:   data,
	})
}

func (h *Handlers) findSite(w http.ResponseWriter, r *http.Request) (proxy.Site, bool) {
	site, err := proxy.Get(r.PathValue("name"))
	if errors.Is(err, proxy.ErrNotFound) {
		http.NotFound(w, r)
		return proxy.Site{}, false
	}
	if err != nil {
		serverError(w, "get site", err)
		return proxy.Site{}, false
	}
	return site, true
}

func proxyInputFromForm(r *http.Request) proxy.Input {
	in := proxy.Input{
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

func proxyErrorOutput(err error) string {
	var testErr *proxy.TestError
	if errors.As(err, &testErr) {
		return testErr.Output
	}
	return err.Error()
}
