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

	"github.com/ph-nixx/site/cmd/web/repos"
	"github.com/redis/go-redis/v9"
)

type App struct {
	Logger *slog.Logger
	tmpl   *template.Template
	repos  *repos.Repos
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

	opts, err := redis.ParseURL(REDIS_URL)
	if err != nil {
		return nil, err
	}

	r, err := repos.New(ctx, repos.FromRedisClient(redis.NewClient(opts)), logger)
	if err != nil {
		return nil, err
	}
	return &App{logger, tmpl, r}, nil
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

	repo := a.repos.Get(ID)
	if len(repo) == 0 || repo[0].SVG == "" {
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
	if err := a.tmpl.ExecuteTemplate(w, "error", data); err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
}

func (a *App) render(w http.ResponseWriter, r *http.Request, tmplName string, data any) {
	w.WriteHeader(http.StatusOK)
	if err := a.tmpl.ExecuteTemplate(w, tmplName, data); err != nil {
		a.renderErr(w, r, http.StatusInternalServerError, err)
	}
}
