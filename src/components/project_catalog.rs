use crate::api::repo_state::RepoState;
use crate::app::Icon;
use chrono::{DateTime, Utc};
use leptos::either::Either;
use leptos::prelude::*;

#[server]
pub async fn list_repos() -> Result<Vec<RepoState>, ServerFnError> {
    use std::sync::{Arc, RwLock};
    let ssr_state = expect_context::<Arc<RwLock<crate::api::SSRState>>>();
    let repos = &ssr_state
        .read()
        .map_err(|e| ServerFnError::new(e.to_string()))?
        .repos;
    Ok(repos.clone())
}

#[component]
pub fn ProjectCatalog() -> impl IntoView {
    // Each repo is planned to expose a webhook the server will use to cache the most recent state in Redis.
    // a row is any set of ProjectCards that have col_spans that sum to 12 or don't specify a col_span

    let repos = Resource::new(|| (), |_| async move { list_repos().await });
    let repo_view = move || {
        Suspend::new(async move {
            match repos.await {
                Ok(v) => {
                    if v.len() == 0 {
                        return Fallback().into_any();
                    }
                    let spans: Vec<u8> = match v.len() {
                        1 => vec![12],
                        2 => vec![12, 12],
                        3 => vec![6, 6, 12],
                        4 => vec![12, 4, 4, 4],
                        _ => vec![6, 6, 4, 4, 4],
                    };
                    let view = spans
                        .into_iter()
                        .zip(v.into_iter())
                        .map(|(col_span, r)| {
                            view! {
                                <li>
                                    <ProjectCard
                                        name=r.name.clone()
                                        href=format!("/docs/{}", r.name)
                                        last_change_utc=r.head_commit.map(|r| r.timestamp)
                                        description=r.description
                                        col_span
                                    />
                                </li>
                            }
                        })
                        .collect_view();
                    view! { <ul>{view}</ul> }.into_any()
                }
                Err(_) => {
                    view! { <span>Well shit, this is not supposed to happen.</span> }.into_any()
                }
            }
        })
    };

    view! {
        <div class="pcatalog">
            <h2>Projects</h2>
            <Suspense fallback=move || Fallback()>
                {repo_view}
            </Suspense>
        </div>
    }
}

/// Compact representation of a Github repo commit state.
#[component]
fn ProjectCard(
    name: String,
    href: String,
    last_change_utc: Option<DateTime<Utc>>,
    description: Option<String>,
    #[prop(optional)] icon: Option<Icon>,
    #[prop(default = 12)] col_span: u8,
    #[prop(optional)] headlines: Option<Vec<(String, String)>>,
) -> impl IntoView {
    let fmt_last_change = match last_change_utc {
        Some(v) => Either::Left({
            let day_delta = (Utc::now().date_naive() - v.date_naive()).num_days();
            let fmt_delta = match day_delta {
                0 => "today".to_string(),
                1 => "yesterday".to_string(),
                _ => format!("{day_delta}d ago"),
            };
            view! { <span>{format!("last change {fmt_delta}")}</span> }
        }),
        None => Either::Right(view! { <></> }),
    };
    let headline_view = match headlines {
        Some(v) => {
            let rows = v
                .into_iter()
                .map(|(headline, content)| {
                    view! {
                        <div>
                            <dt>{headline}</dt>
                            <dd>{content}</dd>
                        </div>
                    }
                    .into_any()
                })
                .collect_view();
            Either::Left(view! { <dl>{rows}</dl> })
        }
        None => Either::Right(view! { <></> }),
    };
    view! {
        <a class=format!("col-span-{col_span}") href=href>
            <div>
                {icon.map(|v| v.into_view())}
                <h3>{name}</h3>
                {fmt_last_change}
            </div>
            <p>{description}</p>
            {headline_view}
        </a>
    }
}

#[component]
fn Fallback() -> impl IntoView {
    view! {
        <div class="pcatalog-skel">
            <ul>
                <li>
                    <div>
                        <div>
                            <span></span>
                            <span><span></span><span></span><span></span><span></span><span></span><span></span><span></span><span></span></span>
                            <span></span>
                        </div>
                        <div>
                            <span></span>
                            <span></span>
                        </div>
                    </div>
                </li>
                <li>
                    <div>
                        <div>
                            <span></span>
                            <span><span></span><span></span><span></span><span></span><span></span><span></span><span></span><span></span></span>
                            <span></span>
                        </div>
                        <div>
                            <span></span>
                            <span></span>
                        </div>
                    </div>
                </li>
                // <li>
                //     <div>
                //         <div>
                //             <span></span>
                //             <span><span></span><span></span><span></span><span></span><span></span><span></span><span></span><span></span></span>
                //             <span></span>
                //         </div>
                //         <div>
                //             <span></span>
                //             <span></span>
                //         </div>
                //     </div>
                // </li>
            </ul>
        </div>
    }
}
