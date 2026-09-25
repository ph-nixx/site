package app

import (
	"html/template"
	"log/slog"
	"net/http"
	"os"
)

type App struct {
	Logger *slog.Logger
	tmpl   *template.Template
}

func New() (*App, error) {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{AddSource: true}))
	tmpl, err := template.ParseGlob("./views/*.html")
	if err != nil {
		return nil, err
	}
	return &App{logger, tmpl}, nil
}

func (a *App) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	fileServe := http.FileServer(http.Dir("./static/"))
	mux.Handle("GET /static/", http.StripPrefix("/static", fileServe))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { a.renderErr(w, r, http.StatusNotFound, nil) })

	mux.HandleFunc("GET /{$}", a.home)

	return mux
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
