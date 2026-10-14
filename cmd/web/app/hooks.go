package app

import (
	"net/http"
	"time"
)

// How long a push's background ingestion may run, independent of the request.
const INGEST_TIMEOUT = 60 * time.Second

// Repo names reserved for site routes; pushes to these repos are dropped before ingestion.
// The exact set is resolved during implementation.
var reservedRepoNames = map[string]struct{}{
	"static": {},
}

// Decodes, filters, and hands off: decodes the body into a github.PushEvent (400 if it does not decode), responds 202,
// and runs Repos.Set in the background for default branch pushes to repos that are not reserved.
func (a *App) githubPushEvent(w http.ResponseWriter, r *http.Request) {
	panic("unimplemented")
}
