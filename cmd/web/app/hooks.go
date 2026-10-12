package app

import "net/http"

// Decodes a GitHub push webhook body into a GithubPushEvent, responding 400 if it does not decode,
// then responds 202 and runs Repos.Set in the background with its own timeout.
func (a *App) githubPushEvent(w http.ResponseWriter, r *http.Request) {
}
