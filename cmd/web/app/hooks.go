package app

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/ph-nixx/site/cmd/web/repos"
)

func (a *App) githubPushEvent(w http.ResponseWriter, r *http.Response) {
	var event repos.GithubPushEvent
	if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
		a.Logger.Error("failed to decode GitHub push event JSON",
			"delivery", r.Header.Get("X-GitHub-Delivery"),
			"err", err,
		)
		http.Error(w, "", http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := a.repos.Set(ctx, event); err != nil {
		http.Error(w, "", http.StatusInternalServerError)
	}
}
