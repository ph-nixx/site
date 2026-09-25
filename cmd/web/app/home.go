package app

import "net/http"

func (a *App) home(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "home", nil)
}
