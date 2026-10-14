package app

import "net/http"

// Serves a docs page at /{repo name}/{segments...}: the doc's HTML alone for an HX-Request, otherwise the full page
// with the sidebar; 404 for an unknown repo and a redirect to /{repo name} for an unknown route.
func (a *App) doc(w http.ResponseWriter, r *http.Request) {
	panic("unimplemented")
}
