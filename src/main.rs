#[cfg(feature = "ssr")]
#[tokio::main]
async fn main() {
    use axum::Router;
    use leptos::logging::log;
    use leptos::prelude::*;
    use leptos_axum::{LeptosRoutes, generate_route_list};
    use site::api;
    use site::api::{SSRState, repo_cache::RepoCache};
    use site::app::{App, shell};
    use tracing_subscriber::fmt;

    fmt().with_max_level(tracing::Level::INFO).init();
    let cache_conn = RepoCache::new().await;
    let cache_conn_ = cache_conn.clone();
    let ssr_state = SSRState::new();
    let ssr_state_ = ssr_state.clone();

    let conf = get_configuration(None).unwrap();
    let addr = conf.leptos_options.site_addr;
    let leptos_options = conf.leptos_options;
    let frontend_routes = generate_route_list(App);
    let app = Router::new()
        .leptos_routes_with_context(
            &leptos_options,
            frontend_routes,
            move || {
                provide_context(ssr_state_.clone());
                provide_context(cache_conn_.clone());
            },
            {
                let leptos_options = leptos_options.clone();
                move || shell(leptos_options.clone())
            },
        )
        .fallback(leptos_axum::file_and_error_handler(shell))
        .with_state(leptos_options);
    let app = app.nest(
        "/api/hooks",
        api::webhooks::init(cache_conn, ssr_state).await,
    );

    // `axum::Server` is a re-export of `hyper::Server`
    log!("listening on http://{}", &addr);
    let listener = tokio::net::TcpListener::bind(&addr).await.unwrap();
    axum::serve(listener, app.into_make_service())
        .await
        .unwrap();
}

#[cfg(not(feature = "ssr"))]
pub fn main() {}
