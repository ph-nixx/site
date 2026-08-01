use crate::api::repo_state::Section;
use leptos::prelude::*;
use leptos_router::hooks::use_params_map;
use std::collections::HashSet;

#[server]
async fn list_sections(repo_name: String) -> Result<Option<Section>, ServerFnError> {
    use std::sync::{Arc, RwLock};
    let ssr_state = expect_context::<Arc<RwLock<crate::api::SSRState>>>();
    let ssr_state = &ssr_state
        .read()
        .map_err(|e| ServerFnError::new(e.to_string()))?;
    match ssr_state.repo_id(repo_name) {
        Some(repo_id) => Ok(ssr_state.sections.get(&repo_id).cloned()),
        _ => Ok(None),
    }
}

#[server]
async fn fetch_doc(
    repo_name: String,
    section_name: Option<String>,
    file_name: Option<String>,
) -> Result<Option<Section>, ServerFnError> {
    use std::sync::{Arc, RwLock};
    let repo_id = {
        let ssr_state = expect_context::<Arc<RwLock<crate::api::SSRState>>>();
        let ssr_state = &ssr_state
            .read()
            .map_err(|e| ServerFnError::new(e.to_string()))?;
        ssr_state.repo_id(repo_name)
    };
    match repo_id {
        Some(repo_id) => {
            let mut cache_conn = expect_context::<crate::api::repo_cache::RepoCache>();
            let v = cache_conn
                .doc(repo_id, section_name, file_name)
                .await
                .map_err(|e| ServerFnError::new(e.to_string()))?;
            Ok(v)
        }
        _ => Ok(None),
    }
}

#[component]
pub fn ProjectDocsLayout() -> impl IntoView {
    let params = use_params_map();
    let set_links = use_context::<WriteSignal<HashSet<String>>>()
        .expect("set_links HashSet<String> signal writer");
    set_links.update(|links| {
        if let Some(v) = params.read().get("repo_name") {
            links.insert(format!("/docs/{v}"));
        }
    });

    let sections = Resource::new(
        move || params.read().get("repo_name").unwrap_or_default(),
        |repo_name| async move { (repo_name.clone(), list_sections(repo_name).await) },
    );
    let view = Suspend::new(async move {
        match sections.await {
            (repo_name, Ok(Some(section))) => view! { <Layout repo_name section /> }.into_any(),
            (_, Ok(None)) => PageNotFound().into_any(),
            (_, Err(_)) => PageNotFound().into_any(),
        }
    });

    view! {
        <div class="docs-shell">
            {view}
        </div>
    }
}

#[component]
fn Layout(repo_name: String, section: Section) -> impl IntoView {
    let params = use_params_map();
    let doc = Resource::new(
        move || {
            (
                params.read().get("repo_name"),
                params.read().get("section_name"),
                params.read().get("file_name"),
            )
        },
        |(repo_name, section_name, file_name)| async move {
            if let Some(repo_name) = repo_name {
                return fetch_doc(repo_name, section_name, file_name).await;
            }
            Ok(None)
        },
    );
    let doc_view = move || {
        Suspend::new(async move {
            match doc.await {
                Ok(Some(doc)) => {
                    let headings_view =
                        doc.headings.map_or_default(|v| {
                            v.into_iter().map(|heading| {
                            let sub_headings = heading.items.into_iter().flatten().map(|sub| {
                                view! { <li><a href=format!("#{}", sub.slug)>{sub.text}</a></li> }
                            }).collect_view();
                            view! {
                                <li>
                                    <a href=format!("#{}", heading.slug)>{heading.text}</a>
                                    <ul>{sub_headings}</ul>
                                </li>
                            }
                        }).collect_view()
                        });
                    view! {
                        <article class="doc">
                            <div inner_html=doc.html/>
                        </article>
                        <aside class="doc-nav">
                            <div>
                                <section>
                                    <a href="#">
                                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m16 18 6-6-6-6"/><path d="m8 6-6 6 6 6"/></svg>
                                        <span>View source</span>
                                    </a>
                                </section>
                                <nav>
                                    <p>On this page</p>
                                    <ul>{headings_view}</ul>
                                </nav>
                            </div>
                        </aside>
                    }
                    .into_any()
                }
                Ok(None) => PageNotFound().into_any(),
                Err(_) => PageNotFound().into_any(),
            }
        })
    };

    let title = section.title.unwrap_or_default();
    view! {
        <aside class="project-nav">
            <details open>
                <summary>
                    <span>{title.clone()}</span>
                    <svg viewBox="0 0 24 24" aria_hidden="true"><path d="m6 9 6 6 6-6"/></svg>
                </summary>
                <nav>
                    <a href=format!("/docs/{repo_name}")>
                        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/><path d="m3.3 7 8.7 5 8.7-5"/><path d="M12 22V12"/></svg>
                        <span>{title}</span>
                    </a>
                    <a href=format!("/docs/{repo_name}")>Overview</a>
                    <SectionsNav sections=section.items repo_name />
                </nav>
            </details>
        </aside>
        <main>
            <Suspense fallback=|| ()>
                {doc_view}
            </Suspense>
        </main>
    }
}

#[component]
fn PageNotFound() -> impl IntoView {
    view! {
        <article class="doc-404">
            <p class aria_hidden="true">404</p>
            <h1>"We couldn't find that page"</h1>
            <p>
                The page might have moved or been renamed, or we hit a temporary error while
                generating it. Check the URL, or head back to the docs and pick up from there.
            </p>
            <a href="/">Back to homepage</a>
        </article>
    }
}

#[component]
fn SectionsNav(sections: Option<Vec<Section>>, repo_name: String) -> impl IntoView {
    sections
        .unwrap_or_default()
        .into_iter()
        .map(|parent| {
            let sub_section_view = parent
                .items
                .into_iter()
                .flatten()
                .filter_map(|item| match item.title {
                    Some(title) => {
                        let href = match (
                            parent.source.clone(),
                            item.source.clone().and_then(|file_name| {
                                Some(file_name.clone().trim_end_matches(".md").to_string())
                            }),
                        ) {
                            (Some(section_name), Some(file_name)) => {
                                format!("/docs/{}/{}/{}", repo_name, section_name, file_name)
                            }
                            _ => "".to_string(),
                        };
                        Some(view! { <li><a href=href>{title}</a></li> })
                    }
                    _ => None,
                })
                .collect_view();
            view! {
                <div>
                    <p>{parent.title}</p>
                    <ul>
                        {sub_section_view}
                    </ul>
                </div>
            }
        })
        .collect_view()
}
