use crate::components::{BuildLog, Hero, Nav, ProjectCatalog, ProjectDocsLayout};
use leptos::prelude::*;
use leptos_meta::{MetaTags, Stylesheet, Title, provide_meta_context};
use leptos_router::{
    components::{ParentRoute, Route, Router, Routes},
    path,
};
use std::fmt;

pub const EMAIL: &'static str = "pmiller0706@gmail.com";
pub const MAILTO_EMAIL: &'static str = "mailto:pmiller0706@gmail.com";
pub const GITHUB_URL: &'static str = "https://github.com/ph-onix";

pub enum Icon {
    RaGithubLogo,
    LuMail,
    LuMenu,
    VsBash,
    LuCopyright,
    LuChevron,
    Link,
}

impl fmt::Display for Icon {
    fn fmt(&self, f: &mut fmt::Formatter) -> fmt::Result {
        match self {
            Icon::RaGithubLogo => write!(f, "RaGithubLogo"),
            Icon::LuMail => write!(f, "LuMail"),
            Icon::LuMenu => write!(f, "LuMenu"),
            Icon::VsBash => write!(f, "VsBash"),
            Icon::LuCopyright => write!(f, "LuCopyright"),
            Icon::LuChevron => write!(f, "LuChevron"),
            Icon::Link => write!(f, "Link"),
        }
    }
}

impl Icon {
    pub fn into_view(self) -> AnyView {
        view! { <svg><use href=format!("#{self}")/></svg>}.into_any()
    }
}

pub fn shell(options: LeptosOptions) -> impl IntoView {
    view! {
        <!DOCTYPE html>
        <html lang="en">
            <head>
                <meta charset="utf-8"/>
                <meta name="viewport" content="width=device-width, initial-scale=1"/>
                <AutoReload options=options.clone()/>
                <HydrationScripts options/>
                <MetaTags/>
            </head>
            <body>
                <App/>
                <svg hidden>
                    <defs>
                        <symbol id=Icon::RaGithubLogo.to_string() viewBox="0 0 15 15">
                            <path d="M7.49933 0.25C3.49635 0.25 0.25 3.49593 0.25 7.50024C0.25 10.703 2.32715 13.4206 5.2081 14.3797C5.57084 14.446 5.70302 14.2222 5.70302 14.0299C5.70302 13.8576 5.69679 13.4019 5.69323 12.797C3.67661 13.235 3.25112 11.825 3.25112 11.825C2.92132 10.9874 2.44599 10.7644 2.44599 10.7644C1.78773 10.3149 2.49584 10.3238 2.49584 10.3238C3.22353 10.375 3.60629 11.0711 3.60629 11.0711C4.25298 12.1788 5.30335 11.8588 5.71638 11.6732C5.78225 11.205 5.96962 10.8854 6.17658 10.7043C4.56675 10.5209 2.87415 9.89918 2.87415 7.12104C2.87415 6.32925 3.15677 5.68257 3.62053 5.17563C3.54576 4.99226 3.29697 4.25521 3.69174 3.25691C3.69174 3.25691 4.30015 3.06196 5.68522 3.99973C6.26337 3.83906 6.8838 3.75895 7.50022 3.75583C8.1162 3.75895 8.73619 3.83906 9.31523 3.99973C10.6994 3.06196 11.3069 3.25691 11.3069 3.25691C11.7026 4.25521 11.4538 4.99226 11.3795 5.17563C11.8441 5.68257 12.1245 6.32925 12.1245 7.12104C12.1245 9.9063 10.4292 10.5192 8.81452 10.6985C9.07444 10.9224 9.30633 11.3648 9.30633 12.0413C9.30633 13.0102 9.29742 13.7922 9.29742 14.0299C9.29742 14.2239 9.42828 14.4496 9.79591 14.3788C12.6746 13.4179 14.75 10.7025 14.75 7.50024C14.75 3.49593 11.5036 0.25 7.49933 0.25Z" fill="currentColor" fill-rule="evenodd" clip-rule="evenodd"/>
                        </symbol>
                        <symbol id=Icon::LuMail.to_string() viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                            <path d="m22 7-8.991 5.727a2 2 0 0 1-2.009 0L2 7"/>
                            <rect x="2" y="4" width="20" height="16" rx="2"/>
                        </symbol>
                        <symbol id=Icon::LuMenu.to_string() viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                            <path d="M4 5h16"/>
                            <path d="M4 12h16"/>
                            <path d="M4 19h16"/>
                        </symbol>
                        <symbol id=Icon::VsBash.to_string() viewBox="0 0 16 16" fill="currentColor">
                            <path d="M13.6547 3.56163L8.91789 0.749977C8.64251 0.586821 8.32832 0.500732 8.00824 0.500732C7.68816 0.500732 7.37396 0.586821 7.09859 0.749977L2.36178 3.56163C2.08017 3.7296 1.84714 3.96798 1.68561 4.25334C1.52408 4.53869 1.43963 4.86118 1.44055 5.18908V10.8124C1.43877 11.1395 1.52198 11.4614 1.68201 11.7467C1.84205 12.032 2.07344 12.2708 2.35351 12.4398L7.09032 15.2515C7.36569 15.4146 7.67989 15.5007 7.99997 15.5007C8.32005 15.5007 8.63424 15.4146 8.90962 15.2515L13.6464 12.4398C13.9265 12.2708 14.1579 12.032 14.3179 11.7467C14.478 11.4614 14.5612 11.1395 14.5594 10.8124V5.18908C14.562 4.86277 14.4801 4.54134 14.3215 4.25613C14.163 3.97091 13.9332 3.73164 13.6547 3.56163ZM9.98632 12.3423V12.7458C9.986 12.7706 9.97948 12.795 9.96735 12.8167C9.95522 12.8383 9.93786 12.8566 9.91685 12.8699L9.67704 13.0071C9.639 13.027 9.60757 13.0071 9.60757 12.9542V12.5573C9.43807 12.6405 9.24411 12.6593 9.06178 12.6102C9.04762 12.6007 9.03722 12.5866 9.03239 12.5703C9.02755 12.554 9.02859 12.5365 9.03532 12.5209L9.12132 12.1554C9.12866 12.1246 9.14542 12.0968 9.16928 12.076C9.17614 12.0693 9.18394 12.0638 9.19244 12.0594C9.19844 12.0568 9.20492 12.0555 9.21146 12.0555C9.218 12.0555 9.22447 12.0568 9.23048 12.0594C9.39772 12.1042 9.57586 12.0817 9.72665 11.9966C9.83381 11.9442 9.92481 11.8638 9.99003 11.764C10.0552 11.6641 10.0923 11.5485 10.0971 11.4293C10.0971 11.2242 9.98466 11.1382 9.71342 11.1366C9.36941 11.1366 9.05186 11.0704 9.04359 10.5626C9.04754 10.3458 9.09941 10.1325 9.19548 9.93807C9.29156 9.74363 9.42946 9.57286 9.5993 9.43798V9.02947C9.59918 9.00433 9.6055 8.97957 9.61767 8.95757C9.62982 8.93557 9.64742 8.91704 9.66877 8.90377L9.90032 8.75657C9.93835 8.73672 9.96978 8.75657 9.96978 8.81115V9.21967C10.1116 9.1551 10.2699 9.13603 10.423 9.16509C10.4389 9.17416 10.4508 9.18892 10.4563 9.20639C10.4618 9.22387 10.4605 9.24279 10.4527 9.25936L10.3717 9.62157C10.3648 9.64915 10.3505 9.67432 10.3303 9.69434C10.3236 9.70156 10.3158 9.7077 10.3072 9.71253C10.2959 9.71631 10.2837 9.71631 10.2724 9.71253C10.1232 9.6798 9.967 9.70523 9.83581 9.78365C9.74024 9.825 9.6584 9.89266 9.59984 9.97876C9.54127 10.0649 9.50839 10.1658 9.50503 10.2699C9.50503 10.4551 9.60261 10.5114 9.93009 10.518C10.3684 10.518 10.5569 10.7165 10.5619 11.1564C10.5578 11.384 10.5042 11.608 10.4048 11.8127C10.3054 12.0175 10.1626 12.1982 9.98632 12.3423ZM12.4672 11.6625C12.47 11.6795 12.4682 11.697 12.4617 11.7131C12.4553 11.7291 12.4446 11.7431 12.4308 11.7535L11.2334 12.4812C11.2278 12.4859 11.2208 12.4888 11.2135 12.4894C11.2062 12.4899 11.1989 12.4881 11.1926 12.4843C11.1864 12.4804 11.1815 12.4747 11.1787 12.4679C11.1759 12.4611 11.1754 12.4536 11.1771 12.4465V12.1372C11.1779 12.1206 11.1835 12.1046 11.1932 12.0911C11.2029 12.0775 11.2162 12.0671 11.2317 12.0611L12.411 11.3549C12.4165 11.3501 12.4235 11.3472 12.4308 11.3467C12.4381 11.3461 12.4455 11.3479 12.4517 11.3518C12.458 11.3556 12.4628 11.3614 12.4656 11.3682C12.4684 11.375 12.4689 11.3825 12.4672 11.3896V11.6625ZM13.2941 4.74749L8.81204 7.51614C8.25302 7.84692 7.84119 8.20913 7.84119 8.88227V14.403C7.84119 14.8066 8.00658 15.0646 8.25467 15.144C8.17284 15.1594 8.08984 15.1677 8.00658 15.1688C7.74336 15.1682 7.48514 15.0968 7.25902 14.9621L2.52221 12.1504C2.29224 12.0111 2.10222 11.8147 1.97064 11.5802C1.83907 11.3458 1.7704 11.0812 1.77133 10.8124V5.18908C1.77012 4.91994 1.83864 4.65508 1.97024 4.4203C2.10183 4.18552 2.292 3.98884 2.52221 3.84941L7.25902 1.03776C7.48434 0.904297 7.7414 0.833879 8.00328 0.833879C8.26516 0.833879 8.52222 0.904297 8.74754 1.03776L13.4843 3.84941C13.6772 3.96611 13.8422 4.12352 13.9679 4.31063C14.0936 4.49775 14.177 4.71006 14.2121 4.93272C14.0583 4.59863 13.7043 4.50601 13.2925 4.74749H13.2941Z"/>
                        </symbol>
                        <symbol id=Icon::LuCopyright.to_string() viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                            <circle cx="12" cy="12" r="10"/>
                            <path d="M14.83 14.83a4 4 0 1 1 0-5.66"/>
                        </symbol>
                        <symbol id=Icon::LuChevron.to_string() viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                            <path d="m9 18 6-6-6-6"/>
                        </symbol>
                        <symbol id=Icon::Link.to_string() viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round">
                            <path d="M15 3h6v6"/>
                            <path d="M10 14 21 3"/>
                            <path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/>
                        </symbol>
                    </defs>
                </svg>
            </body>
        </html>
    }
}

#[component]
pub fn App() -> impl IntoView {
    provide_meta_context();

    view! {
        <Stylesheet id="leptos" href="/pkg/site.css"/>
        <Title text="title"/>
        <Router>
            <main>
                <Routes fallback=|| "Page not found.".into_view()>
                    <ParentRoute path=path!("") view=Nav>
                        <Route path=path!("") view=Home/>
                        <ParentRoute path=path!("docs") view=ProjectDocsLayout>
                            <Route path=path!(":repo_name") view=|| ()/>
                            <Route path=path!(":repo_name/") view=|| ()/>
                            <Route path=path!(":repo_name/:section_name") view=|| ()/>
                            <Route path=path!(":repo_name/:section_name/") view=|| ()/>
                            <Route path=path!(":repo_name/:section_name/:file_name") view=|| ()/>
                            <Route path=path!(":repo_name/:section_name/:file_name/") view=|| ()/>
                        </ParentRoute>
                    </ParentRoute>
                </Routes>
            </main>
        </Router>
    }
}

#[component]
fn Home() -> impl IntoView {
    view! {
        <Hero />
        <div class="home-content nav-align">
            <ProjectCatalog/>
            <BuildLog />
            <footer role="contentinfo" aria_label="Footer">
                <p>
                    <span>{Icon::LuCopyright.into_view()} Pheonx Miller</span>
                    <span aria-hidden>"—"</span>
                    <a href="https://leptos.dev" target="_blank" rel="noreferrer noopener">Built With Leptos</a>
                    <a href=format!("{GITHUB_URL}/site") target="_blank" rel="noreferrer noopener">Source</a>
                </p>
                <div>
                    <a href=MAILTO_EMAIL>
                        <span aria_hidden>{Icon::LuMail.into_view()}</span>
                        {EMAIL}
                    </a>
                    <a href=GITHUB_URL target="_blank" rel="noreferrer noopener" aria_label="GitHub">
                        <span aria_hidden>{Icon::RaGithubLogo.into_view()}</span>
                    </a>
                </div>
            </footer>
        </div>
    }
}
