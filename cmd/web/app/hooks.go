package app

import (
	"context"
	"net/http"
	"time"

	"github.com/google/go-github/v91/github"
)

// How long a push's background ingestion may run, independent of the request.
const INGEST_TIMEOUT = 60 * time.Second

// Repo names reserved for site routes; pushes to these repos are dropped before ingestion.
// The exact set is resolved during implementation.
var reservedRepoNames = map[string]struct{}{
	"static": {},
}

// Verifies, decodes, filters, and hands off: responds 401 to a delivery not signed with the webhook secret, 204 to an
// event other than push, and 400 to a body that does not decode, then responds 202 and runs Repos.Set in the
// background for default branch pushes to repos that are not reserved.
func (a *App) githubEvent(w http.ResponseWriter, r *http.Request) {
	// anyone can reach this endpoint, and a forged event could name any repo and commit, so only a delivery GitHub
	// signed with the webhook secret is acted on; ValidatePayload also caps the body at GitHub's 25 MB payload limit
	payload, err := github.ValidatePayload(r, a.webhookSecret)
	if err != nil {
		a.Logger.Warn("rejected webhook delivery", "remote", r.RemoteAddr, "err", err)
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}

	event, err := github.ParseWebHook(github.WebHookType(r), payload)
	if err != nil {
		a.Logger.Warn("failed to parse webhook delivery", "err", err)
		return
	}

	switch e := event.(type) {
	case *github.PushEvent:
		w.WriteHeader(http.StatusAccepted)
		repo := e.GetRepo()
		if e.GetRef() != "refs/heads/"+repo.GetDefaultBranch() {
			return
		}

		if _, reserved := reservedRepoNames[repo.GetName()]; reserved {
			a.Logger.Warn("dropped push to a reserved repo name", "repo", repo.GetFullName())
			return
		}

		// the request's context ends with the response, so ingestion gets its own deadline
		go func() {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), INGEST_TIMEOUT)
			defer cancel()
			if err := a.repos.SetPushEvent(ctx, e); err != nil {
				a.Logger.Error("ingestion failed", "repo", repo.GetFullName(), "after", e.GetAfter(), "err", err)
			}
		}()
	case *github.RepositoryEvent:
	case *github.PingEvent:
	default:
		a.Logger.Info("skipped unspecified github webhook type", "type", github.WebHookType(r))
	}

}
