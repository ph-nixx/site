package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/google/go-github/v91/github"
	"github.com/ph-nixx/site/cmd/web/repos"
	"github.com/redis/go-redis/v9"
)

type App struct {
	Logger        *slog.Logger
	tmpl          *template.Template
	repos         *repos.Repos
	webhookSecret []byte // GITHUB_WEBHOOK_SECRET, read once by New; signs every push delivery
}

// The data the nav template renders: every repo for the project menu, and the first URL segment, which names the
// project being viewed ("" on the home page).
type navData struct {
	Repos   []*repos.Repo
	Current string
}

// funcMap exposes helpers to the HTML templates.
var funcMap = template.FuncMap{"colSpan": colSpan}

func New(ctx context.Context, logger *slog.Logger) (*App, error) {
	tmpl, err := template.New("").Funcs(funcMap).ParseGlob("./views/*.html")
	if err != nil {
		return nil, err
	}

	REDIS_URL, ok := os.LookupEnv("REDIS_URL")
	if !ok {
		return nil, errors.New("unset env var REDIS_URL")
	}

	// go-github skips the signature check when the secret is empty, so an empty one is as unsafe as none
	secret, ok := os.LookupEnv("GITHUB_WEBHOOK_SECRET")
	if !ok || secret == "" {
		return nil, errors.New("unset env var GITHUB_WEBHOOK_SECRET")
	}

	token, ok := os.LookupEnv("GITHUB_TOKEN")
	if !ok || token == "" {
		return nil, errors.New("unset env var GITHUB_TOKEN")
	}

	opts, err := redis.ParseURL(REDIS_URL)
	if err != nil {
		return nil, err
	}

	gh, err := github.NewClient(github.WithAuthToken(token))
	if err != nil {
		return nil, err
	}

	r, err := repos.New(ctx, repos.FromRedisClient(redis.NewClient(opts)), repos.FromGithubClient(gh), logger)
	if err != nil {
		return nil, err
	}
	return &App{Logger: logger, tmpl: tmpl, repos: r, webhookSecret: []byte(secret)}, nil
}

func (a *App) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	fileServe := http.FileServer(http.Dir("./static/"))
	mux.Handle("GET /static/", http.StripPrefix("/static", fileServe))
	mux.HandleFunc("GET /static/icons/{ID}", a.icon)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		a.renderErr(w, r, http.StatusNotFound, nil)
	})

	mux.HandleFunc("GET /{$}", a.home)
	mux.HandleFunc("GET /{name}", a.doc)
	mux.HandleFunc("GET /{name}/{route...}", a.doc)
	mux.HandleFunc("POST /hooks/github", a.githubPushEvent)

	return mux
}

// icon serves a repo's cached SVG with validators so browsers can cache it
// and revalidate cheaply (304) once the max-age expires.
func (a *App) icon(w http.ResponseWriter, r *http.Request) {
	ID, err := strconv.ParseInt(r.PathValue("ID"), 10, 64)
	if err != nil || ID < 1 {
		http.NotFound(w, r)
		return
	}

	repo, ok := a.repos.Get(ID)
	if !ok || repo[0].SVG == "" {
		http.NotFound(w, r)
		return
	}

	svg := []byte(repo[0].SVG)
	sum := sha256.Sum256(svg)
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:8])+`"`)
	w.Header().Set("Cache-Control", "public, max-age=3600")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(svg))
}

func (a *App) renderErr(w http.ResponseWriter, r *http.Request, statusCode int, err error) {
	if err != nil {
		a.Logger.Error(err.Error(), "method", r.Method, "URI", r.URL.RequestURI())
	}
	data := struct {
		StatusCode int
		Message    string
	}{
		StatusCode: statusCode,
		Message:    http.StatusText(statusCode),
	}
	// rendered into a buffer first, so the status is written only once the page is known to render
	var buf bytes.Buffer
	if err := a.tmpl.ExecuteTemplate(&buf, "error", data); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(statusCode)
	buf.WriteTo(w)
}

func (a *App) render(w http.ResponseWriter, r *http.Request, tmplName string, data any) {
	// rendered into a buffer first, so a template error can still be answered with a 500
	var buf bytes.Buffer
	if err := a.tmpl.ExecuteTemplate(&buf, tmplName, data); err != nil {
		a.renderErr(w, r, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	buf.WriteTo(w)
}
