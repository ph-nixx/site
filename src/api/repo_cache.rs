use crate::api::repo_state::{Commit, Heading, Repo, RepoState, Section};
use comrak::nodes::{NodeCodeBlock, NodeHeading, NodeValue};
use comrak::{Anchorizer, Arena, Options, format_html, parse_document};
use core::fmt;
use core::time::Duration;
use redis::aio::MultiplexedConnection;
use redis::{AsyncCommands, Msg, RedisError};
use reqwest;
use reqwest::StatusCode;
use serde::Deserialize;
use std::sync::Arc;
use std::{collections::HashSet, env, ops::Not};
use thiserror::Error;
use tokio::sync::{broadcast, mpsc::unbounded_channel};
use tokio::task::JoinSet;
use tracing;
use tracing::Instrument;

#[derive(Debug, Error)]
pub enum CachingError {
    #[error("redis operation failed during caching {0}")]
    Redis(#[from] RedisError),
    #[error("content failed to parse {0}")]
    BadJson(#[from] serde_json::Error),
    #[error("http {0}")]
    HTTP(#[from] reqwest::Error),
    #[error("failed to parse md to html")]
    MdParsing(#[from] fmt::Error),
}

/// Manages Github repository commit and documentation state in a Redis data store.
///
/// A single instance is provided to the Leptos router via [`provide_context`] at startup,
/// making it available to server functions through [`use_context`] during SSR and to
/// webhook handlers when GitHub pushes updates.
#[derive(Clone)]
pub struct RepoCache {
    client: redis::aio::MultiplexedConnection,
    broadcast: broadcast::Sender<String>,
}

#[derive(Deserialize)]
struct PushEvent {
    #[serde(rename = "ref")]
    git_ref: String,
    repository: Repo,
    commits: Vec<Commit>,
    head_commit: Option<Commit>,
}

impl RepoCache {
    /// Connect to the data store you want to cache github webhook info in.
    pub async fn new() -> Self {
        let (redis_pubs, mut rx) = unbounded_channel();
        let config = redis::AsyncConnectionConfig::new().set_push_sender(redis_pubs);
        let redis_url = env::var("REDIS_URL").expect("envvar REDIS_URL");
        let client = redis::Client::open(redis_url)
            .expect("failed to connect to redis")
            .get_multiplexed_async_connection_with_config(&config)
            .await
            .expect("failed to connect to redis");

        let (broadcast, _) = broadcast::channel(256);
        let broadcaster = broadcast.clone();
        tokio::spawn(async move {
            while let Some(push) = rx.recv().await {
                match Msg::from_push_info(push) {
                    Some(msg) => match msg.get_channel() {
                        Ok(channel) => {
                            let _ = broadcaster.send(channel);
                        }
                        _ => continue,
                    },
                    _ => continue,
                }
            }
        });
        Self { client, broadcast }
    }

    /// Get a subset of every cached RepoState.
    ///
    /// If no names are provided you will get the entire set.
    pub async fn repos(&mut self, ids: Option<Vec<u64>>) -> Result<Vec<RepoState>, CachingError> {
        match ids {
            Some(ids) => {
                let repo_states = self
                    .client
                    .hmget::<_, _, Vec<Option<String>>>("repos:states", ids)
                    .await?
                    .iter()
                    .filter_map(|opt| match opt {
                        Some(v) => serde_json::from_str(v).ok(),
                        _ => None,
                    })
                    .collect();
                Ok(repo_states)
            }
            None => {
                let repo_states = self
                    .client
                    .hvals::<_, Vec<String>>("repos:states")
                    .await?
                    .iter()
                    .filter_map(|s| match serde_json::from_str(s) {
                        Ok(v) => Some(v),
                        Err(e) => {
                            tracing::warn!(
                                hash = "repos:states",
                                value = %s,
                                error = %e,
                                "dropping malformed cached repo state",
                            );
                            None
                        }
                    })
                    .collect();
                Ok(repo_states)
            }
        }
    }

    /// Get up to the cache limit of the most recent commits from a repo in reverse chronological order.
    ///
    /// If no repo ids are provided I will give you a repo agnostic commits in chronological order.
    pub async fn commits(
        &mut self,
        ids: Option<Vec<u64>>,
        limit: isize,
    ) -> Result<Vec<Commit>, CachingError> {
        let limit_index = limit - 1;
        match ids {
            Some(v) => {
                if v.len() == 0 {
                    return Ok(vec![]);
                }
                let commits = v
                    .iter()
                    .fold(&mut redis::pipe(), |p, id| {
                        p.zrange(format!("commits:{id}"), 0, limit_index)
                    })
                    .query_async::<Vec<Vec<String>>>(&mut self.client)
                    .await?
                    .iter()
                    .flatten()
                    .filter_map(|s| serde_json::from_str(&s).ok())
                    .collect();
                Ok(commits)
            }
            None => {
                let commits = self
                    .client
                    .zrange::<_, Vec<String>>("commits", 0, limit_index)
                    .await?
                    .iter()
                    .filter_map(|s| match serde_json::from_str(s) {
                        Ok(v) => Some(v),
                        Err(e) => {
                            tracing::warn!(
                                hash = "repos:states",
                                value = %s,
                                error = %e,
                                "dropping malformed cached repo state",
                            );
                            None
                        }
                    })
                    .collect();
                Ok(commits)
            }
        }
    }

    /// Fetch the html representation of a doc md file in a repos `docs/`.
    pub async fn doc(
        &mut self,
        repo_id: u64,
        section_name: Option<String>,
        file_name: Option<String>,
    ) -> Result<Option<Section>, CachingError> {
        let key = match (section_name, file_name) {
            (Some(s), Some(f)) => format!("{repo_id}:docs:{s}:{f}.md"),
            (Some(s), _) => format!("{repo_id}:docs:{s}:overview.md"),
            (_, Some(f)) => format!("{repo_id}:docs:{f}.md"),
            _ => format!("{repo_id}:docs:overview.md"),
        };
        let result = match self
            .client
            .hget::<_, _, Option<String>>("repos:docs".to_string(), key)
            .await?
        {
            Some(v) => {
                let section = serde_json::from_str::<Section>(&v)?;
                Some(section)
            }
            _ => None,
        };
        Ok(result)
    }

    /// Update a repo state and the commit log in response to a webhook event and notify
    /// any subscribers.
    ///
    /// I only consider events that occur on the default branch.
    ///
    /// NOTE: My job is to parse and cache fields from a JSON string
    ///       with a schema I have been defined on; it's up to the caller to validate the source
    ///       before calling me.
    pub async fn cache_event(&mut self, payload: String) -> Result<(), CachingError> {
        let mut v = serde_json::from_str::<PushEvent>(&payload).map_err(|e| {
            tracing::error!(
                error = %e,
                payload_head = %payload.chars().take(20).collect::<String>(),
                "failed to parse github event",
            );
            e
        })?;
        if v.git_ref != format!("refs/heads/{}", v.repository.default_branch) {
            return Ok(());
        }
        let repo_id = v.repository.id;
        let repo_name = v.repository.name.clone();
        let new_repo_state = RepoState {
            id: repo_id,
            language: v.repository.language.clone(),
            name: repo_name.clone(),
            description: v.repository.description.clone(),
            head_commit: v.head_commit,
        };
        let new_repo_state = serde_json::to_string(&new_repo_state)?;
        let score_member_pairs: Vec<(i64, String)> = v
            .commits
            .iter_mut()
            .filter_map(|c| {
                c.repo_name = repo_name.clone();
                if let Ok(json) = serde_json::to_string(c) {
                    return Some((c.timestamp.timestamp() * -1, json));
                }
                None
            })
            .collect();

        let conn = self.client.clone();
        tokio::spawn(async move {
            if let Err(e) = reconcile_docs(conn, v.repository, v.commits).await {
                tracing::error!(repo_id, error = ?e, "couldn't reconcile doc");
            }
        });
        let repo_channel = format!("repos:{repo_id}:state");
        let commits_key = format!("{}:commits", repo_id);
        let commit_log = "commits";
        let cap: isize = 50;
        let cap_index = cap - 1;
        let _: () = redis::pipe()
            .atomic()
            .hset("repos:states", repo_id, &new_repo_state)
            .ignore()
            .zadd_multiple(&commits_key, &score_member_pairs)
            .ignore()
            .zremrangebyrank(&commits_key, cap_index, -1)
            .ignore()
            .zadd_multiple(&commit_log, &score_member_pairs)
            .ignore()
            .zremrangebyrank(&commit_log, cap_index, -1)
            .ignore()
            .publish(&repo_channel, 0)
            .query_async(&mut (*self).client)
            .await?;
        Ok(())
    }

    /// On any repo state change made by another RepoCache instance
    /// `f` will be called as a background task.
    ///
    /// If provided an id I will watch that repo's channel otherwise I will watch
    /// every repo state change.
    ///
    /// PLANNED: remove deleted files from the cache
    pub async fn repo_subscribe<F, Fut>(&mut self, id: Option<u64>, mut f: F)
    where
        F: FnMut(String) -> Fut + Send + 'static,
        Fut: Future<Output = ()> + Send + 'static,
    {
        match id {
            Some(v) => {
                let channel = format!("repos:{}:state", v);
                self.client.subscribe(&channel).await
            }
            _ => {
                let channel = "repos:*".to_string();
                self.client.psubscribe(&channel).await
            }
        }
        .expect(&format!("failed to subsribe to repo id {:?}", id));

        let mut rx = self.broadcast.subscribe();
        tokio::spawn(async move {
            loop {
                if let Ok(channel) = rx.recv().await {
                    f(channel).await;
                }
            }
        });
    }
}

/// Update the cache and site routes to reflect any changed or created md files
/// registered in `docs/overview.md` json frontmatter.
// 1. optimistically update the json frontmatter string in the cache and publish the change
//    so the servers have fresh routes
// 2. collect all the file paths that need to be cached or recached
// 3. start futures for each file path that fetches the raw utf8 file contents,
//    parses the GFM to html and caches the result
#[tracing::instrument(skip(cache_conn, commits), fields(repo_id = repo.id))]
async fn reconcile_docs(
    mut cache_conn: MultiplexedConnection,
    repo: Repo,
    commits: Vec<Commit>,
) -> Result<(), CachingError> {
    let http_client = reqwest::Client::builder()
        .timeout(Duration::new(10, 0))
        .build()
        .expect("create reqwest client");

    let base_url = if let Some(v) = commits.last() {
        format!(
            "https://raw.githubusercontent.com/{}/{}",
            repo.full_name, v.id
        )
    } else {
        return Ok(());
    };

    let res = http_client
        .get(format!("{base_url}/docs/overview.md"))
        .send()
        .await
        .map_err(|e| {
            tracing::warn!(
                url = %format!("{base_url}/docs/overview.md"),
                timeout = e.is_timeout(),
                connect = e.is_connect(),
                error = %e,
                "overview.md request failed",
            );
            e
        })?;
    let text = match res.status() {
        StatusCode::OK => res.text().await?,
        _ => return Ok(()),
    };
    // split on the md front matter delim
    let Some((fm, md)) = text.split_once("---") else {
        return Ok(());
    };
    let mut head_overview = serde_json::from_str::<Section>(fm).map_err(|e| {
        tracing::error!(
            repo_id = repo.id,
            base_url = base_url,
            frontmatter = %fm,
        );
        e
    })?;
    let options = Arc::new({
        // GFM -> HTML config
        let mut options = Options::default();
        options.extension.strikethrough = true;
        options.extension.table = true;
        options.extension.autolink = true;
        options.extension.tasklist = true;
        options.extension.tagfilter = true;
        options.extension.footnotes = true;
        options.extension.header_id_prefix = Some(String::new());
        options
    });
    to_html_with_headings(md, options.clone()).map(|(html, headings)| {
        head_overview.html = Some(html);
        head_overview.headings = headings;
    })?;

    let head_overview_str = serde_json::to_string(&head_overview)?;
    let cached_overview_str = cache_conn
        .hget::<_, _, Option<String>>("repos:docs", format!("{}:docs:overview.md", repo.id))
        .await?;
    let _: () = redis::pipe()
        .hset(
            "repos:docs",
            format!("{}:docs:overview.md", repo.id),
            head_overview_str,
        )
        .publish(format!("repos:{}:docs", repo.id), 0)
        .query_async(&mut cache_conn)
        .await?;
    let cached_items = match cached_overview_str {
        Some(v) => match serde_json::from_str::<Section>(&v).ok() {
            Some(v) => v.items,
            _ => None,
        },
        _ => None,
    };
    let filepaths = match head_overview.items {
        Some(head_items) => match filepaths_to_update(head_items, cached_items, commits) {
            Some(v) => v,
            _ => return Ok(()),
        },
        _ => return Ok(()),
    };

    let mut join_set: JoinSet<Result<(), CachingError>> = JoinSet::new();
    filepaths.for_each(|fp| {
        let base_url = base_url.clone();
        let options = options.clone();
        let mut client = cache_conn.clone();
        let http_client = http_client.clone();
        let span = tracing::info_span!("cache_doc", repo_id = repo.id, file = %fp);
        join_set.spawn(
            async move {
                let res = http_client
                    .get(format!("{base_url}/{fp}"))
                    .send()
                    .await
                    .map_err(|e| {
                        tracing::error!(
                            url = %format!("{base_url}/docs/overview.md"),
                            timeout = e.is_timeout(),
                            connect = e.is_connect(),
                            error = ?e,
                            "overview.md request failed",
                        );
                        e
                    })?;
                let res = res.error_for_status().map_err(|e| {
                    tracing::error!(file = %fp, status = ?e.status(), "non-success status");
                    e
                })?;

                let res = res.text().await?;
                let section =
                    to_html_with_headings(&res, options).map(|(html, headings)| Section {
                        html: Some(html),
                        headings: headings,
                        title: None,
                        source: None,
                        items: None,
                    })?;
                let section = serde_json::to_string::<Section>(&section)?;
                let key = fp.replace('/', ":");
                let _: () = client
                    .hset("repos:docs", format!("{}:{}", repo.id, key), section)
                    .await?;
                Ok(())
            }
            .instrument(span),
        );
    });
    while let Some(res) = join_set.join_next().await {
        match res {
            Ok(Err(e)) => tracing::error!(error = %e, "could not to cache file"),
            Err(e) => tracing::error!(error = %e, "task panic"),
            _ => {}
        }
    }
    Ok(())
}

/// Parse a GFM file into html and return the top level slugified headings.
fn to_html_with_headings(
    md: &str,
    options: Arc<Options>,
) -> Result<(String, Option<Vec<Heading>>), fmt::Error> {
    let arena = Arena::new();
    let root = parse_document(&arena, md, &options);
    let mut anchorizer = Anchorizer::new();
    let mut headings: Vec<Heading> = Vec::new();
    let mut nodes = root.descendants().into_iter().peekable();
    while let Some(node) = nodes.next() {
        let level = match &node.data.borrow().value {
            NodeValue::Heading(NodeHeading { level, .. }) => *level,
            _ => continue,
        };
        let text = node.collect_text();
        let slug = anchorizer.anchorize(&text);
        if level != 1 || slug.len() == 0 {
            continue;
        }

        let mut heading_hierarchy: Vec<Heading> = vec![];
        while let Some(&node) = nodes.peek() {
            let level = match &node.data.borrow().value {
                NodeValue::Heading(NodeHeading { level, .. }) => *level,
                _ => {
                    nodes.next();
                    continue;
                }
            };
            if level == 1 {
                break;
            }

            let Some(node) = nodes.next() else {
                break;
            };
            let text = node.collect_text();
            let slug = anchorizer.anchorize(&text);
            if level != 2 || slug.len() == 0 {
                continue;
            }
            heading_hierarchy.push(Heading {
                text,
                slug,
                items: None,
            });
        }
        headings.push(Heading {
            text,
            slug,
            items: Some(heading_hierarchy),
        });
    }

    let mut html = String::new();
    format_html(root, &options, &mut html)?;
    if headings.len() > 0 {
        return Ok((html, Some(headings)));
    }
    Ok((html, None))
}

/// Extract the set of files that need to be cached or recached.
///
/// I use `docs/overview.md` json fronmatter as the source of truth when determining
/// the list of md files that are eligble for caching.
/// We sort to ensure the ordering invariant but Github commits are usually sorted oldest to newest.
fn filepaths_to_update(
    head_overview: Vec<Section>,
    cached_overview: Option<Vec<Section>>,
    mut commits: Vec<Commit>,
) -> Option<impl Iterator<Item = String>> {
    let registered = head_overview
        .iter()
        .filter_map(|dir| match dir.source.as_ref() {
            Some(dir_name) => {
                let iter = dir.items.iter().flat_map(move |v| {
                    v.iter().filter_map(move |file| match file.source.as_ref() {
                        Some(file_name) => {
                            let path = if file_name.ends_with(".md") {
                                format!("docs/{dir_name}/{file_name}")
                            } else {
                                format!("docs/{dir_name}/{file_name}.md")
                            };
                            Some(path)
                        }
                        _ => None,
                    })
                });
                Some(iter)
            }
            _ => None,
        })
        .flatten()
        .collect::<HashSet<String>>();

    // when no cached overview exists we just generate all the file valid file paths
    // from the extracted head overview.md
    if let None = cached_overview {
        return Some(registered.into_iter());
    }

    // collect file paths from commits history
    let mut update = HashSet::<String>::new();
    commits.sort_by_key(|v| v.timestamp);
    let mut deleted = HashSet::<String>::new();
    for mut commit in commits.into_iter().rev() {
        // we can make this cleaner with .extend
        commit.modified.extend(commit.added.into_iter());
        for fp in commit.modified {
            if registered.contains(&fp) && deleted.contains(&fp).not() {
                update.insert(fp);
            }
        }
        for fp in commit.removed.into_iter() {
            deleted.insert(fp);
        }
    }

    // collect filepaths by turning both schemas in to sets of (dir_name, file_name)
    // and take the set difference {x in head_overview} - {y in cached_set}
    // this gives of everything that is in head but not in the cache
    let head_paths = head_overview
        .iter()
        .filter_map(|dir| match dir.source.as_ref() {
            Some(dir_name) => {
                let iter = dir.items.iter().flat_map(move |v| {
                    v.iter()
                        .filter_map(move |child| match child.source.as_ref() {
                            Some(child_name) => Some((dir_name, child_name)),
                            _ => None,
                        })
                });
                Some(iter)
            }
            _ => None,
        })
        .flatten()
        .collect::<HashSet<(&String, &String)>>();

    let cached_paths = cached_overview
        .as_ref()?
        .iter()
        .filter_map(|dir| match dir.source.as_ref() {
            Some(dir_name) => {
                let iter = dir.items.iter().flat_map(move |v| {
                    v.iter()
                        .filter_map(move |child| match child.source.as_ref() {
                            Some(child_name) => Some((dir_name, child_name)),
                            _ => None,
                        })
                });
                Some(iter)
            }
            _ => None,
        })
        .flatten()
        .collect::<HashSet<(&String, &String)>>();

    let dif = head_paths
        .difference(&cached_paths)
        .into_iter()
        .map(|(dir, file)| {
            if file.ends_with(".md") {
                format!("docs/{dir}/{file}")
            } else {
                format!("docs/{dir}/{file}.md")
            }
        });
    update.extend(dif);
    Some(update.into_iter())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::api::repo_state::*;
    use chrono::*;

    #[test]
    fn parses_json_correctly() -> () {
        let x = r#"
            {
              "ref": "refs/heads/main",
              "before": "fac7aaec1ab758fac59ceb774c2ac595de550d10",
              "after": "b1c974978a2c744965bba2a4ff406a4e95c94c66",
              "repository": {
                "id": 1055909254,
                "node_id": "R_kgDOPu_lhg",
                "name": "nvim",
                "full_name": "ph-onix/nvim",
                "private": false,
                "owner": {},
                "html_url": "https://github.com/ph-onix/nvim",
                "description": null,
                "url": "https://api.github.com/repos/ph-onix/nvim",
                "commits_url": "https://api.github.com/repos/ph-onix/nvim/commits{/sha}",
                "created_at": 1757732043,
                "updated_at": "2026-07-05T06:37:30Z",
                "pushed_at": 1784068160,
                "open_issues_count": 0,
                "license": {},
                "allow_forking": true,
                "is_template": false,
                "language": "pussy",
                "web_commit_signoff_required": false
              },
              "pusher": {
                "name": "ph-onix",
                "email": "184308910+ph-onix@users.noreply.github.com"
              },
              "sender": {},
              "created": false,
              "deleted": false,
              "compare": "https://github.com/ph-onix/nvim/compare/fac7aaec1ab7...b1c974978a2c",
              "commits": [
                {
                  "id": "b1c974978a2c744965bba2a4ff406a4e95c94c66",
                  "tree_id": "d7aadc16e73813d3fb2c57e7f2f4e07302f7ef62",
                  "distinct": true,
                  "message": "chore",
                  "timestamp": "2026-07-14T17:29:14-05:00",
                  "url": "https://github.com/ph-onix/nvim/commit/b1c974978a2c744965bba2a4ff406a4e95c94c66",
                  "author": {
                    "name": "ph-onix",
                    "email": "pmiller0706@gmail.com",
                    "date": "2026-07-14T17:29:14-05:00",
                    "username": "ph-onix"
                  },
                  "committer": {},
                  "added": [],
                  "removed": [],
                  "modified": [
                    "README.md"
                  ]
                }
              ],
              "head_commit": {
                "id": "b1c974978a2c744965bba2a4ff406a4e95c94c66",
                "tree_id": "d7aadc16e73813d3fb2c57e7f2f4e07302f7ef62",
                "distinct": true,
                "message": "chore",
                "timestamp": "2026-07-14T17:29:14-05:00",
                "url": "https://github.com/ph-onix/nvim/commit/b1c974978a2c744965bba2a4ff406a4e95c94c66",
                "author": {
                  "name": "ph-onix",
                  "email": "pmiller0706@gmail.com",
                  "date": "2026-07-14T17:29:14-05:00",
                  "username": "ph-onix"
                },
                "committer": {},
                "added": [],
                "removed": [],
                "modified": [
                  "README.md"
                ]
              }
            }
        "#;
        let x = serde_json::from_str::<PushEvent>(&x).unwrap();
        let repo_state = RepoState {
            id: x.repository.id,
            name: x.repository.name,
            language: x.repository.language,
            description: x.repository.description,
            head_commit: x.head_commit,
        };
        let expect = r#"{
            "id": 1055909254,
            "name": "nvim",
            "language": "pussy",
            "description": null,
            "head_commit": {
                "id": "b1c974978a2c744965bba2a4ff406a4e95c94c66",
                "distinct": true,
                "message": "chore",
                "timestamp": "2026-07-14T17:29:14-05:00",
                "author": {
                  "username": "ph-onix",
                  "email": "pmiller0706@gmail.com"
                },
                "added": [],
                "removed": [],
                "modified": [
                  "README.md"
                ]
            }
        }"#;
        let expect = serde_json::from_str::<RepoState>(&expect).unwrap();
        let expect = serde_json::to_string(&expect).unwrap();
        let result = serde_json::to_string(&repo_state).unwrap();
        assert_eq!(result, expect);
    }

    #[test]
    fn can_parse_array_of_json_strings() {
        let _ = vec![
            RepoState {
                id: 1,
                name: "personal-site".into(),
                language: Some("Rust".into()),
                description: Some("My Leptos personal site".into()),
                head_commit: Some(Commit {
                    id: "a1b2c3d".into(),
                    repo_name: "".into(),
                    timestamp: Utc.with_ymd_and_hms(2026, 7, 16, 9, 30, 0).unwrap(),
                    author: Author {
                        username: "ph-onix".into(),
                        email: "pheonixmiller@industrialacqai.com".into(),
                    },
                    distinct: true,
                    message: "feat: complete build log UI".into(),
                    added: vec!["src/components/term".into()],
                    modified: vec!["src/app.rs".into(), "src/lib.rs".into()],
                    removed: vec!["src/repo_state.rs".into()],
                }),
            },
            RepoState {
                id: 2,
                name: "claw".into(),
                language: Some("Rust".into()),
                description: None,
                head_commit: Some(Commit {
                    id: "e4f5g6h".into(),
                    repo_name: "".into(),
                    timestamp: Utc::now(),
                    author: Author {
                        username: "ph-onix".into(),
                        email: "pheonixmiller@industrialacqai.com".into(),
                    },
                    distinct: true,
                    message: "chore: initial scaffol".into(),
                    added: vec!["Cargo.toml".into(), "src/main.rs".into()],
                    modified: vec![],
                    removed: vec![],
                }),
            },
            RepoState {
                id: 3,
                name: "dotfiles".into(),
                language: None,
                description: Some("Shell + editor co".into()),
                head_commit: None,
            },
        ]
        .iter()
        .for_each(|expect| {
            let v = serde_json::to_string(&expect).unwrap();
            let result: RepoState = serde_json::from_str(&v).unwrap();
            assert_eq!(result, *expect);
        });
    }

    fn file(source: &str) -> Section {
        Section {
            title: None,
            source: Some(source.into()),
            items: None,
            html: None,
            headings: None,
        }
    }

    fn dir(source: &str, children: Vec<Section>) -> Section {
        Section {
            title: None,
            source: Some(source.into()),
            items: Some(children),
            html: None,
            headings: None,
        }
    }

    fn commit_at(ts: DateTime<Utc>, added: &[&str], modified: &[&str], removed: &[&str]) -> Commit {
        Commit {
            id: String::new(),
            repo_name: String::new(),
            timestamp: ts,
            author: Author {
                username: String::new(),
                email: String::new(),
            },
            distinct: true,
            message: String::new(),
            added: added.iter().map(|s| s.to_string()).collect(),
            modified: modified.iter().map(|s| s.to_string()).collect(),
            removed: removed.iter().map(|s| s.to_string()).collect(),
        }
    }

    fn run(
        head: Vec<Section>,
        cached: Option<Vec<Section>>,
        commits: Vec<Commit>,
    ) -> HashSet<String> {
        filepaths_to_update(head, cached, commits)
            .map(|it| it.collect())
            .unwrap_or_default()
    }

    #[test]
    fn only_collects_files_registered_in_frontmatter() {
        // cached: what the previous overview.md registered
        let cached = vec![
            dir("guides", vec![file("intro.md"), file("setup")]), // `setup` is extension-less (B2)
            dir("api", vec![file("auth.md")]),
        ];
        // head: adds a brand-new registration `webhooks.md` under `api`
        let head = vec![
            dir("guides", vec![file("intro.md"), file("setup")]),
            dir("api", vec![file("auth.md"), file("webhooks.md")]), // new vs cached
        ];

        let older = commit_at(
            Utc.with_ymd_and_hms(2026, 7, 20, 9, 0, 0).unwrap(),
            &[],
            &[
                "docs/guides/intro.md", // registered, not later removed -> included (happy path)
                "docs/api/auth.md", // modified here, but removed in `newer` -> excluded (shadow)
            ],
            &[],
        );
        let newer = commit_at(
            Utc.with_ymd_and_hms(2026, 7, 20, 12, 0, 0).unwrap(),
            &[],
            &[
                "docs/guides/setup.md",  // registered as ext-less `setup` -> included (B2)
                "docs/archive/auth.md",  // registered *filename*, UNregistered dir -> excluded (B1)
                "README.md",             // not under docs/ -> excluded
                "docs/guides/notes.txt", // not .md -> excluded
                "docs/guides/draft.md",  // .md under docs/, unregistered -> excluded
            ],
            &["docs/api/auth.md"], // shadows the older modify -> auth.md excluded
        );

        let got = run(head, Some(cached), vec![newer, older]);

        // Labeled guards so a failure names the bug directly:
        assert!(
            got.contains("docs/guides/setup.md"),
            "B2: extension-less registration dropped on modify"
        );
        assert!(
            !got.contains("docs/archive/auth.md"),
            "B1: filename-only match pulled in an unregistered dir"
        );
        assert!(
            !got.contains("docs/api/auth.md"),
            "shadow: modified-then-removed file was collected"
        );
        assert!(
            got.contains("docs/api/webhooks.md"),
            "diff: newly registered doc missing"
        );
        assert!(
            got.contains("docs/guides/intro.md"),
            "happy path: registered modify dropped"
        );

        // Full contract: exactly these, nothing extra.
        let want: HashSet<String> = [
            "docs/guides/intro.md",
            "docs/guides/setup.md",
            "docs/api/webhooks.md",
        ]
        .into_iter()
        .map(String::from)
        .collect();
        assert_eq!(got, want);
    }

    #[test]
    fn only_collects_files_registered_in_frontmatter_with_a_empty_cache() {
        let head = vec![
            dir("guides", vec![file("intro.md"), file("setup")]), // ext-less -> `.md` appended
            dir("api", vec![file("auth.md")]),
            // dir with no children contributes nothing
            Section {
                title: None,
                source: Some("orphan".into()),
                items: None,
                html: None,
                headings: None,
            },
            // dir with no source name is skipped entirely
            Section {
                title: None,
                source: None,
                items: Some(vec![file("ghost.md")]),
                html: None,
                headings: None,
            },
        ];
        // Cold cache MUST ignore commits: this modify/remove pair changes nothing.
        let commits = vec![commit_at(
            Utc.with_ymd_and_hms(2026, 7, 20, 12, 0, 0).unwrap(),
            &[],
            &["docs/guides/intro.md"],
            &["docs/api/auth.md"],
        )];

        let got = run(head, None, commits);

        let want: HashSet<String> = [
            "docs/guides/intro.md",
            "docs/guides/setup.md",
            "docs/api/auth.md",
        ]
        .into_iter()
        .map(String::from)
        .collect();
        assert_eq!(got, want);
    }
}

#[cfg(test)]
mod to_html_tests {
    use super::*;

    struct Expected {
        text: &'static str,
        children: &'static [&'static str],
    }

    struct Case {
        name: &'static str,
        md: &'static str,
        /// Value for `extension.header_id_prefix`; `Some("")` is what production uses.
        prefix: Option<&'static str>,
        want: &'static [Expected],
    }

    struct Rendered {
        level: u32,
        id: String,
        text: String,
    }

    const CASES: &[Case] = &[
        Case {
            name: "control: sibling h1s with no nesting",
            md: "# Alpha\n\n# Beta\n",
            prefix: Some(""),
            want: &[
                Expected {
                    text: "Alpha",
                    children: &[],
                },
                Expected {
                    text: "Beta",
                    children: &[],
                },
            ],
        },
        Case {
            name: "h2s belong to the h1 above them & all non h1s preceding the first h1 are ignored",
            md: "## Orphan\n\n# Alpha\n\n## A-one\n\n# Beta\n\n## B-one\n",
            prefix: Some(""),
            want: &[
                Expected {
                    text: "Alpha",
                    children: &["A-one"],
                },
                Expected {
                    text: "Beta",
                    children: &["B-one"],
                },
            ],
        },
        Case {
            name: "anchorizer: a duplicate h3 must not shift the next h1's slug suffix",
            md: "# Intro\n\n### Intro\n\n# Intro\n",
            prefix: Some(""),
            want: &[
                Expected {
                    text: "Intro",
                    children: &[],
                },
                Expected {
                    text: "Intro",
                    children: &[],
                },
            ],
        },
        Case {
            name: "text: a code span contributes its literal to the label and the slug",
            md: "# The `docs/` convention\n",
            prefix: Some(""),
            want: &[Expected {
                text: "The docs/ convention",
                children: &[],
            }],
        },
        Case {
            name: "text: a setext soft break is a word boundary",
            md: "Deploy and\nrollback\n==========\n",
            prefix: Some(""),
            want: &[Expected {
                text: "Deploy and rollback",
                children: &[],
            }],
        },
        Case {
            // Flip this expectation if quoted headings should stay out of the nav.
            name: "containers: a heading inside a blockquote is still addressable",
            md: "# Alpha\n\n> ## Quoted\n",
            prefix: Some(""),
            want: &[Expected {
                text: "Alpha",
                children: &["Quoted"],
            }],
        },
        Case {
            name: "empty slug: a heading that anchorizes to nothing is not listed",
            md: "# Alpha\n\n## 🚀\n\n# Beta\n",
            prefix: Some(""),
            want: &[
                Expected {
                    text: "Alpha",
                    children: &[],
                },
                Expected {
                    text: "Beta",
                    children: &[],
                },
            ],
        },
    ];

    fn opts(prefix: Option<&str>) -> Arc<Options<'_>> {
        let mut options = Options::default();
        options.extension.strikethrough = true;
        options.extension.table = true;
        options.extension.autolink = true;
        options.extension.tasklist = true;
        options.extension.tagfilter = true;
        options.extension.footnotes = true;
        options.extension.header_id_prefix = prefix.map(String::from);
        Arc::new(options)
    }

    fn attr(fragment: &str, name: &str) -> Option<String> {
        let needle = format!(" {name}=\"");
        let start = fragment.find(&needle)? + needle.len();
        let end = start + fragment[start..].find('"')?;
        Some(fragment[start..end].to_string())
    }

    fn unescape(v: &str) -> String {
        v.replace("&quot;", "\"")
            .replace("&lt;", "<")
            .replace("&gt;", ">")
            .replace("&amp;", "&")
    }

    fn strip_tags(fragment: &str) -> String {
        let mut out = String::new();
        let mut depth = 0usize;
        for c in fragment.chars() {
            match c {
                '<' => depth += 1,
                '>' => depth = depth.saturating_sub(1),
                _ if depth == 0 => out.push(c),
                _ => {}
            }
        }
        unescape(&out)
    }

    fn rendered_headings(html: &str) -> Vec<Rendered> {
        let mut out = Vec::new();
        let mut i = 0;
        while let Some(rel) = html[i..].find("<h") {
            let start = i + rel;
            let level = match html[start + 2..]
                .chars()
                .next()
                .and_then(|c| c.to_digit(10))
            {
                Some(l) if (1..=6).contains(&l) => l,
                _ => {
                    i = start + 2;
                    continue;
                }
            };
            let Some(tag_end) = html[start..].find('>').map(|r| start + r) else {
                break;
            };
            let id = attr(&html[start..tag_end], "id").unwrap_or_default();
            let close = format!("</h{level}>");
            let Some(content_end) = html[tag_end..].find(&close).map(|r| tag_end + r) else {
                break;
            };
            let content = &html[tag_end + 1..content_end];
            let text = match attr(content, "data-heading-content") {
                Some(v) => unescape(&v),
                None => strip_tags(content),
            };
            out.push(Rendered { level, id, text });
            i = content_end + close.len();
        }
        out
    }

    /// Flatten the returned heading tree into (level, slug, text) in document order.
    fn flatten(headings: &Option<Vec<Heading>>) -> Vec<(u32, String, String)> {
        let mut out = Vec::new();
        for h in headings.iter().flatten() {
            out.push((1, h.slug.clone(), h.text.clone()));
            for c in h.items.iter().flatten() {
                out.push((2, c.slug.clone(), c.text.clone()));
            }
        }
        out
    }

    fn flatten_want(want: &[Expected]) -> Vec<(u32, String)> {
        want.iter()
            .flat_map(|e| {
                std::iter::once((1, e.text.to_string()))
                    .chain(e.children.iter().map(|c| (2, c.to_string())))
            })
            .collect()
    }

    fn check(case: &Case) -> Result<(), String> {
        let (html, headings) = to_html_with_headings(case.md, opts(case.prefix))
            .map_err(|e| format!("render: {e}"))?;

        let actual = flatten(&headings);
        let labels: Vec<(u32, String)> = actual.iter().map(|(l, _, t)| (*l, t.clone())).collect();
        let want = flatten_want(case.want);
        if labels != want {
            return Err(format!(
                "toc shape/labels\n    want: {want:?}\n    got:  {labels:?}"
            ));
        }

        let rendered: Vec<Rendered> = rendered_headings(&html)
            .into_iter()
            .filter(|r| r.level <= 2)
            .collect();
        let mut cursor = 0;
        for (_, slug, text) in &actual {
            let Some(offset) = rendered[cursor..].iter().position(|r| &r.text == text) else {
                return Err(format!(
                    "heading {text:?} is in the toc but not in the rendered html at or after position {cursor}"
                ));
            };
            let found = &rendered[cursor + offset];
            if &found.id != slug {
                return Err(format!(
                    "heading {text:?} has slug {slug:?} but the rendered id is {:?}",
                    found.id
                ));
            }
            cursor += offset + 1;
        }

        let mut seen = HashSet::new();
        for (_, slug, text) in &actual {
            if slug.is_empty() {
                return Err(format!("heading {text:?} produced an empty slug"));
            }
            if seen.insert(slug.clone()).not() {
                return Err(format!("slug {slug:?} is used by more than one heading"));
            }
        }
        Ok(())
    }

    #[test]
    fn to_html_with_headings_contract() {
        let failures: Vec<String> = CASES
            .iter()
            .filter_map(|c| check(c).err().map(|e| format!("  {}\n    {e}", c.name)))
            .collect();
        assert!(
            failures.is_empty(),
            "{} of {} cases failed:\n\n{}\n",
            failures.len(),
            CASES.len(),
            failures.join("\n\n"),
        );
    }
}
