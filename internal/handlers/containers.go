package handlers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"os/exec"
	"time"

	"aura/internal/containers"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

type containersListData struct {
	Containers    []containers.Container
	DockerMissing bool
}

type containersIndexData struct {
	List      containersListData
	Flash     string
	FlashType string
}

func loadContainersList() (containersListData, error) {
	data := containersListData{}
	list, err := containers.ListWithStats()
	if errors.Is(err, containers.ErrDockerMissing) {
		data.DockerMissing = true
		return data, nil
	}
	if err != nil {
		return data, err
	}
	data.Containers = list
	return data, nil
}

var containerFlashes = map[string]string{
	"started":   "Container started.",
	"stopped":   "Container stopped.",
	"restarted": "Container restarted.",
	"removed":   "Container removed.",
	"failed":    "Container action failed. Check the server logs.",
}

type containerMountRow struct {
	containers.Mount
	FilesURL string
}

type containersShowData struct {
	ID            string
	Container     *containers.Details
	Mounts        []containerMountRow
	Stats         *containers.Stats
	FilesURL      string
	DockerMissing bool
	Flash         string
	FlashType     string
}

func (h *Handlers) ContainersShow(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !containers.ValidID(id) {
		http.NotFound(w, r)
		return
	}
	data := containersShowData{ID: id}
	details, err := containers.Inspect(id)
	switch {
	case errors.Is(err, containers.ErrDockerMissing):
		data.DockerMissing = true
	case errors.Is(err, containers.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		serverError(w, "inspect container", err)
		return
	}
	title := "Container"
	if details != nil {
		data.Container = details
		title = details.Name()
		data.FilesURL = h.containerFilesURL(details.RootFolder())
		for _, m := range details.Mounts {
			data.Mounts = append(data.Mounts, containerMountRow{Mount: m, FilesURL: h.containerFilesURL(m.Source)})
		}
		if details.Running() {
			stats, err := containers.ContainerStats(details.ID)
			if err != nil {
				log.Printf("container stats %s: %v", id, err)
			}
			data.Stats = stats
		}
	}
	status := r.URL.Query().Get("status")
	data.Flash = containerFlashes[status]
	data.FlashType = "success"
	if status == "failed" {
		data.FlashType = "danger"
	}
	h.render(w, http.StatusOK, "containers_show.html", page{
		Title:  title,
		Active: "containers",
		Data:   data,
	})
}

func (h *Handlers) ContainersIndex(w http.ResponseWriter, r *http.Request) {
	list, err := loadContainersList()
	if err != nil {
		serverError(w, "list containers", err)
		return
	}
	data := containersIndexData{List: list}
	status := r.URL.Query().Get("status")
	data.Flash = containerFlashes[status]
	data.FlashType = "success"
	if status == "failed" {
		data.FlashType = "danger"
	}
	h.render(w, http.StatusOK, "containers_index.html", page{
		Title:  "Containers",
		Active: "containers",
		Data:   data,
	})
}

func (h *Handlers) ContainersList(w http.ResponseWriter, r *http.Request) {
	list, err := loadContainersList()
	if err != nil {
		serverError(w, "list containers", err)
		return
	}
	w.Header().Set("X-Containers-List", "1")
	h.render(w, http.StatusOK, "containers_list", list)
}

func (h *Handlers) ContainersStart(w http.ResponseWriter, r *http.Request) {
	h.containerAction(w, r, "start", containers.Start, "started")
}

func (h *Handlers) ContainersStop(w http.ResponseWriter, r *http.Request) {
	h.containerAction(w, r, "stop", containers.Stop, "stopped")
}

func (h *Handlers) ContainersRestart(w http.ResponseWriter, r *http.Request) {
	h.containerAction(w, r, "restart", containers.Restart, "restarted")
}

func (h *Handlers) ContainersRemove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !containers.ValidID(id) {
		http.NotFound(w, r)
		return
	}
	if err := containers.Remove(id); err != nil {
		log.Printf("remove container %s: %v", id, err)
		http.Redirect(w, r, containerReturnURL(r, id, "failed"), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/containers?status=removed", http.StatusSeeOther)
}

func (h *Handlers) containerFilesURL(path string) string {
	if path == "" {
		return ""
	}
	resolved, err := h.files.Resolve(path)
	if err != nil {
		return ""
	}
	return "/files?" + url.Values{"path": {resolved}}.Encode()
}

func containerReturnURL(r *http.Request, id, status string) string {
	if r.FormValue("return") == "show" {
		return "/containers/" + url.PathEscape(id) + "?status=" + status
	}
	return "/containers?status=" + status
}

func (h *Handlers) ContainersLogs(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !containers.ValidID(id) {
		http.NotFound(w, r)
		return
	}
	conn, err := shellUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("logs upgrade %s: %v", id, err)
		return
	}
	defer conn.Close()
	cmd, err := containers.Logs(id, 1000)
	if err != nil {
		conn.WriteMessage(websocket.BinaryMessage, []byte(err.Error()+"\r\n"))
		return
	}
	out := &wsWriter{conn: conn}
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		log.Printf("logs start %s: %v", id, err)
		conn.WriteMessage(websocket.BinaryMessage, []byte("Failed to read logs: "+err.Error()+"\r\n"))
		return
	}
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				cmd.Process.Kill()
				return
			}
		}
	}()
	cmd.Wait()
	conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
}

type wsWriter struct {
	conn *websocket.Conn
}

func (w *wsWriter) Write(p []byte) (int, error) {
	if err := w.conn.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

var shellUpgrader = websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096}

type shellMessage struct {
	Type string `json:"type"`
	Data string `json:"data"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

func (h *Handlers) ContainersShell(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !containers.ValidID(id) {
		http.NotFound(w, r)
		return
	}
	conn, err := shellUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("shell upgrade %s: %v", id, err)
		return
	}
	defer conn.Close()
	cmd, err := containers.Shell(id)
	if err != nil {
		conn.WriteMessage(websocket.BinaryMessage, []byte(err.Error()+"\r\n"))
		return
	}
	servePTY(conn, cmd, id)
}

func servePTY(conn *websocket.Conn, cmd *exec.Cmd, label string) {
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		log.Printf("shell start %s: %v", label, err)
		conn.WriteMessage(websocket.BinaryMessage, []byte("Failed to start shell: "+err.Error()+"\r\n"))
		return
	}
	defer func() {
		ptmx.Close()
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		cmd.Wait()
	}()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		conn.Close()
	}()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg shellMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		switch msg.Type {
		case "input":
			ptmx.Write([]byte(msg.Data))
		case "resize":
			if msg.Cols > 0 && msg.Rows > 0 {
				pty.Setsize(ptmx, &pty.Winsize{Rows: msg.Rows, Cols: msg.Cols})
			}
		}
	}
}

func (h *Handlers) containerAction(w http.ResponseWriter, r *http.Request, action string, fn func(string) error, status string) {
	id := r.PathValue("id")
	if !containers.ValidID(id) {
		http.NotFound(w, r)
		return
	}
	if err := fn(id); err != nil {
		log.Printf("%s container %s: %v", action, id, err)
		http.Redirect(w, r, containerReturnURL(r, id, "failed"), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, containerReturnURL(r, id, status), http.StatusSeeOther)
}
