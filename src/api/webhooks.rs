use super::repo_cache::RepoCache;
use super::repo_state::{Commit, RepoState, Section};
use axum::Router;
use axum::extract::State;
use axum::http::{HeaderMap, StatusCode};
use axum::routing::post;
use hex;
use hmac::{Hmac, KeyInit, Mac};
use sha2::Sha256;
use std::collections::HashMap;
use std::env;
use std::sync::{Arc, RwLock};
use tokio;
use tokio::task::JoinSet;

type HmacSha256 = Hmac<Sha256>;

/// A data store & interface that allows components to use server side date.
///
/// * sections: a map of repo names to (`section` directory name, `section` md file names)
///             a `section` is a top level directory in a repo's `docs/` that only contains md files
pub struct SSRState {
    pub repos: Vec<RepoState>,
    pub commits: Vec<Commit>,
    pub sections: HashMap<u64, Section>,
    // cache_conn: RepoCache,
}

impl SSRState {
    pub fn new() -> Arc<RwLock<Self>> {
        let ssr_state = Self {
            repos: vec![],
            commits: vec![],
            sections: HashMap::new(),
        };
        Arc::new(RwLock::new(ssr_state))
    }

    pub fn repo_id(&self, repo_name: String) -> Option<u64> {
        for repo in self.repos.iter() {
            if repo_name == repo.name {
                return Some(repo.id);
            }
        }
        None
    }

    async fn refresh(mut conn: RepoCache) -> Option<(Vec<Commit>, Vec<RepoState>)> {
        let mut conn2 = conn.clone();
        match tokio::join!(conn.commits(None, 7), conn2.repos(None)) {
            (Ok(commits), Ok(repos)) => Some((commits, repos)),
            _ => None,
        }
    }
}

pub async fn init(mut cache_conn: RepoCache, ssr_state: Arc<RwLock<SSRState>>) -> Router<()> {
    // need to populate cache when the routes are created
    let cache_conn_ = cache_conn.clone();
    let ssr_state_ = ssr_state.clone();
    cache_conn
        .repo_subscribe(None, move |channel| {
            let mut cache_conn_ = cache_conn_.clone();
            let ssr_state_ = ssr_state_.clone();
            async move {
                let parts: Vec<&str> = channel.splitn(4, ':').collect();
                let (Some(&id), Some(&data)) = (parts.get(1), parts.get(2)) else {
                    return;
                };
                let Ok(repo_id) = id.parse() else { return };
                match data {
                    "state" => {
                        let Some((commits, repos)) = SSRState::refresh(cache_conn_).await else {
                            tracing::error!("failed to fetch new SSRState");
                            return;
                        };
                        let mut v = ssr_state_.write().unwrap_or_else(|e| e.into_inner());
                        v.commits = commits;
                        v.repos = repos;
                    }
                    "docs" => {
                        let sections = match cache_conn_.doc(repo_id, None, None).await {
                            Ok(Some(v)) => v,
                            Ok(None) => {
                                tracing::warn!(repo_id, "no doc found");
                                return ();
                            }
                            Err(e) => {
                                tracing::error!(repo_id, ?e);
                                return ();
                            }
                        };
                        let mut v = ssr_state_.write().unwrap_or_else(|e| e.into_inner());
                        v.sections.insert(repo_id, sections);
                    }
                    _ => (),
                }
            }
        })
        .await;

    {
        let (commits, repos) = SSRState::refresh(cache_conn.clone())
            .await
            .expect("fetch initial SSRState");
        let mut v = ssr_state.write().unwrap_or_else(|e| e.into_inner());
        v.commits = commits;
        v.repos = repos;
    }
    let repo_ids = {
        let v = ssr_state.read().unwrap_or_else(|e| e.into_inner());
        v.repos.iter().map(|repo| repo.id).collect::<Vec<u64>>()
    };
    let mut join_set = JoinSet::new();
    repo_ids.iter().for_each(|&id| {
        let mut conn = cache_conn.clone();
        join_set.spawn(async move {
            let repo_sections = conn.doc(id, None, None).await;
            (id, repo_sections)
        });
    });

    let mut sections: HashMap<u64, Section> = HashMap::new();
    while let Some(v) = join_set.join_next().await {
        match v {
            Ok((repo_id, Ok(Some(repo_sections)))) => {
                sections.insert(repo_id, repo_sections);
            }
            Ok((_, Ok(None))) => {}
            Ok((_, Err(e))) => tracing::error!(?e),
            Err(e) => tracing::error!(?e),
        }
    }
    let mut v = ssr_state.write().unwrap_or_else(|e| e.into_inner());
    v.sections = sections;

    Router::new()
        .route("/github", post(github_event))
        .with_state(cache_conn)
}

/// Ingest incoming repo state change and `git push` events into the cache.
async fn github_event(
    headers: HeaderMap,
    State(mut r): State<RepoCache>,
    body: String,
) -> Result<StatusCode, StatusCode> {
    let event = headers
        .get("X-GitHub-Event")
        .and_then(|v| v.to_str().ok())
        .filter(|v| v.starts_with("push") || v.starts_with("ping"))
        .ok_or_else(|| StatusCode::BAD_REQUEST)?;

    let xhub_sig = headers
        .get("X-Hub-Signature-256")
        .and_then(|v| v.to_str().ok())
        .ok_or_else(|| StatusCode::BAD_REQUEST)?
        .strip_prefix("sha256=")
        .ok_or_else(|| StatusCode::BAD_REQUEST)?;
    let xhub_sig = hex::decode(xhub_sig).map_err(|_| StatusCode::BAD_REQUEST)?;
    let webhook_secret = env::var("WEBHOOK_SECRET").expect("envvar WEBHOOK_SECRET");

    let mut mac = HmacSha256::new_from_slice(webhook_secret.as_bytes())
        .map_err(|_| StatusCode::INTERNAL_SERVER_ERROR)?;
    mac.update(body.as_bytes());
    mac.verify_slice(&xhub_sig)
        .map_err(|_| StatusCode::BAD_REQUEST)?;

    if event.starts_with("push") {
        r.cache_event(body).await.map_err(|e| {
            tracing::error!(?e);
            StatusCode::INTERNAL_SERVER_ERROR
        })?;
    }
    Ok(StatusCode::NO_CONTENT)
}
