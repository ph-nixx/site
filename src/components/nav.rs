use crate::app::{EMAIL, GITHUB_URL, Icon, MAILTO_EMAIL};
use leptos::prelude::*;
use leptos_router::components::Outlet;
use leptos_router::hooks::use_location;
use std::collections::HashSet;

#[component]
pub fn Nav() -> impl IntoView {
    let repos = Resource::new(|| (), |_| async move { super::list_repos().await });
    let repo_view = move || {
        Suspend::new(async move {
            match repos.await {
                Ok(repos) => {
                    let view = repos
                        .into_iter()
                        .map(|repo| view! { <li><NavMenuRow name=repo.name.clone() href=format!("/docs/{}", repo.name) /></li> })
                        .collect_view();
                    Some(view)
                }
                Err(_) => None,
            }
        })
    };
    let contacts = vec![
        (EMAIL, MAILTO_EMAIL, Icon::LuMail),
        ("Github", GITHUB_URL, Icon::RaGithubLogo),
    ]
    .into_iter()
    .map(|(name, href, icon)| view! { <li><NavMenuRow name=name.to_string() href=href.to_string() icon /></li> })
    .collect_view();

    let (links, set_links) = signal(HashSet::<String>::new());
    provide_context(links);
    provide_context(set_links);

    view! {
        <div class="nav-sticky">
            <nav aria-label="Primary">
                <div>
                    <Crumbs />
                    <button popovertarget="nav-menu" aria_label="Open menu">
                        {Icon::LuMenu.into_view()}
                    </button>
                    <div id="nav-menu" class="nav-menu" popover aria_label="Menu">
                        <p>Projects</p>
                        <Suspense fallback=|| ()>
                            <ul>{repo_view}</ul>
                        </Suspense>
                        <hr/>
                        <ul>{contacts}</ul>
                    </div>
                </div>
            </nav>
        </div>
        <Outlet />
    }
}

#[component]
fn NavMenuRow(
    name: String,
    href: String, // follows the WHATWG URL standard to be consistent with pathname.get()
    #[prop(optional)] icon: Option<Icon>,
) -> impl IntoView {
    let location = use_location();
    let href_ = href.clone();
    let selected = move || {
        let path = location.pathname.get();
        path.starts_with(&href_).to_string()
    };
    view! {
        <a href=href aria_current=selected>
            {match icon { Some(v) => v.into_view(), None => view! { <></> }.into_any()}}
            <span>{name}</span>
        </a>
    }
}

#[component]
fn Crumbs() -> impl IntoView {
    let links =
        use_context::<ReadSignal<HashSet<String>>>().expect("a links HashSet<String> signal");
    let location = use_location();
    let route_view = move || match location
        .pathname
        .get()
        .trim_end_matches('/')
        .rsplit_once('/')
    {
        Some((_, "")) | None => {
            vec![view! { <li><span aria_current="page">home</span></li> }.into_any()]
        }
        Some((active_href, active_route)) => {
            let mut href = String::new();
            let mut result = vec![view! { <li><a href="/">home</a></li> }.into_any()];
            active_href
                .split('/')
                .filter(|s| !s.is_empty())
                .for_each(|route| {
                    href.push('/');
                    href.push_str(route);
                    if links.read().contains(&href) {
                        result.push(view! { <li><a href=href.clone()>{route}</a></li> }.into_any());
                    } else {
                        result.push(view! { <li>{route}</li> }.into_any());
                    }
                });
            result.push(
                view! { <li><span aria_current="page">{active_route}</span></li> }.into_any(),
            );
            result
        }
    };
    view! {
        <nav class="crumbs" aria-label="Breadcrumb">
            <ol>{route_view}</ol>
        </nav>
    }
}
