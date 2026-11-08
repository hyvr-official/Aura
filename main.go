package main

import (
	"log"
	"net/http"
	"os"

	"aura/internal/auth"
	"aura/internal/backups"
	"aura/internal/config"
	"aura/internal/console"
	"aura/internal/database"
	"aura/internal/files"
	"aura/internal/handlers"
	"aura/internal/projects"
	"aura/internal/repositories"
	"aura/web"
)

var version = "dev"

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	db, err := database.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer db.Close()

	store := backups.NewStore(db)
	runner := backups.NewRunner(store)

	if len(os.Args) > 1 && os.Args[1] == "backup" {
		runner.RunAll(backups.TriggerCron)
		return
	}

	sessions := auth.NewSessionStore(db)
	passwords := auth.NewPasswordStore(db)
	if !passwords.IsSet() {
		log.Printf("No password set, open /setup to create one")
	}
	h := handlers.New(cfg, sessions, passwords, web.Templates(), store, runner, files.New(cfg.FilesRoot), repositories.NewStore(db), repositories.NewGit(), projects.NewStore(db), projects.NewDocker(), console.NewStore(db))
	protected := func(fn http.HandlerFunc) http.Handler { return auth.RequireAuth(sessions, fn) }

	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", h.LoginForm)
	mux.HandleFunc("POST /login", h.Login)
	mux.HandleFunc("POST /logout", h.Logout)
	mux.HandleFunc("GET /setup", h.SetupForm)
	mux.HandleFunc("POST /setup", h.Setup)
	mux.HandleFunc("POST /setup/checks", h.SetupChecks)
	mux.Handle("GET /{$}", protected(h.Dashboard))
	mux.Handle("GET /dashboard/stats", protected(h.DashboardStats))
	mux.Handle("GET /system/usage", protected(h.SystemUsage))

	mux.Handle("GET /backups", protected(h.BackupsIndex))
	mux.Handle("GET /backups/create", protected(h.BackupsCreate))
	mux.Handle("POST /backups", protected(h.BackupsStore))
	mux.Handle("GET /backups/{id}", protected(h.BackupsShow))
	mux.Handle("POST /backups/{id}/run", protected(h.BackupsRun))
	mux.Handle("GET /backups/{id}/edit", protected(h.BackupsEdit))
	mux.Handle("POST /backups/{id}", protected(h.BackupsUpdate))
	mux.Handle("POST /backups/{id}/delete", protected(h.BackupsDelete))

	mux.Handle("GET /containers", protected(h.ContainersIndex))
	mux.Handle("GET /containers/list", protected(h.ContainersList))
	mux.Handle("POST /containers/{id}/start", protected(h.ContainersStart))
	mux.Handle("GET /containers/{id}", protected(h.ContainersShow))
	mux.Handle("POST /containers/{id}/stop", protected(h.ContainersStop))
	mux.Handle("POST /containers/{id}/restart", protected(h.ContainersRestart))
	mux.Handle("POST /containers/{id}/delete", protected(h.ContainersRemove))
	mux.Handle("GET /containers/{id}/logs", protected(h.ContainersLogs))
	mux.Handle("GET /containers/{id}/shell", protected(h.ContainersShell))

	mux.Handle("GET /volumes", protected(h.VolumesIndex))
	mux.Handle("POST /volumes", protected(h.VolumesStore))
	mux.Handle("POST /volumes/{name}/delete", protected(h.VolumesDelete))

	mux.Handle("GET /files", protected(h.FilesIndex))
	mux.Handle("GET /files/content", protected(h.FilesContent))
	mux.Handle("GET /files/download", protected(h.FilesDownload))
	mux.Handle("POST /files/create", protected(h.FilesCreate))
	mux.Handle("POST /files/rename", protected(h.FilesRename))
	mux.Handle("POST /files/delete", protected(h.FilesDelete))
	mux.Handle("POST /files/save", protected(h.FilesSave))

	mux.Handle("GET /proxy", protected(h.ProxyIndex))
	mux.Handle("GET /proxy/create", protected(h.ProxyCreate))
	mux.Handle("POST /proxy", protected(h.ProxyStore))
	mux.Handle("GET /proxy/{name}/edit", protected(h.ProxyEdit))
	mux.Handle("POST /proxy/{name}", protected(h.ProxyUpdate))
	mux.Handle("POST /proxy/{name}/enable", protected(h.ProxyEnable))
	mux.Handle("POST /proxy/{name}/disable", protected(h.ProxyDisable))
	mux.Handle("POST /proxy/{name}/delete", protected(h.ProxyDelete))
	mux.Handle("POST /proxy/{name}/ssl", protected(h.ProxySSL))
	mux.Handle("GET /proxy/{name}/logs", protected(h.ProxyLogs))

	mux.Handle("GET /repositories", protected(h.RepositoriesIndex))
	mux.Handle("GET /repositories/create", protected(h.RepositoriesCreate))
	mux.Handle("POST /repositories", protected(h.RepositoriesStore))
	mux.Handle("GET /repositories/{id}", protected(h.RepositoriesShow))
	mux.Handle("GET /repositories/{id}/edit", protected(h.RepositoriesEdit))
	mux.Handle("POST /repositories/{id}", protected(h.RepositoriesUpdate))
	mux.Handle("POST /repositories/{id}/key", protected(h.RepositoriesKey))
	mux.Handle("POST /repositories/{id}/clone", protected(h.RepositoriesClone))
	mux.Handle("POST /repositories/{id}/pull", protected(h.RepositoriesPull))
	mux.Handle("POST /repositories/{id}/checkout", protected(h.RepositoriesCheckout))
	mux.Handle("POST /repositories/{id}/delete", protected(h.RepositoriesDelete))

	mux.Handle("GET /projects", protected(h.ProjectsIndex))
	mux.Handle("GET /projects/create", protected(h.ProjectsCreate))
	mux.Handle("POST /projects", protected(h.ProjectsStore))
	mux.Handle("GET /projects/{id}", protected(h.ProjectsShow))
	mux.Handle("GET /projects/{id}/edit", protected(h.ProjectsEdit))
	mux.Handle("POST /projects/{id}", protected(h.ProjectsUpdate))
	mux.Handle("POST /projects/{id}/pull", protected(h.ProjectsPull))
	mux.Handle("POST /projects/{id}/deploy", protected(h.ProjectsDeploy))
	mux.Handle("POST /projects/{id}/down", protected(h.ProjectsDown))
	mux.Handle("POST /projects/{id}/delete", protected(h.ProjectsDelete))

	mux.Handle("GET /console", protected(h.ConsoleIndex))
	mux.Handle("GET /console/shell", protected(h.ConsoleShell))
	mux.Handle("GET /console/commands", protected(h.ConsoleCommandsIndex))
	mux.Handle("POST /console/commands", protected(h.ConsoleCommandsStore))
	mux.Handle("POST /console/commands/{id}", protected(h.ConsoleCommandsUpdate))
	mux.Handle("POST /console/commands/{id}/delete", protected(h.ConsoleCommandsDelete))

	mux.Handle("GET /settings", protected(h.SettingsIndex))
	mux.Handle("POST /settings/checks", protected(h.SettingsChecks))

	addr := ":" + cfg.Port
	log.Printf("Aura %s listening on http://localhost%s", version, addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
