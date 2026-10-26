---
feature_name: bread_crumbs
start_date: 2026-10-26
status: implemented
---

## Summary
[summary]: #summary

The breadcrumb in the site nav works like a stack of the routes you have visited, one crumb per URL segment.
Following a link deeper pushes the new page's segments onto the stack, even when the link skips several levels at
once; going back up to a crumb keeps the segments below it, so you can backtrack down the trail as well as up it.
A crumb is clickable only when its segments form a route that renders a page: `home` (`/`), a project
(`/{repo}`), or a docs node with a `path` in `docs.toml`.
A heading only chapter has a segment but no page, so its crumb is plain text.

For example, from `/site` you follow a sidebar link straight to `/site/guide/macos`, skipping `/site/guide`:

home / site / guide / **macos**

`guide` gets a crumb even though you never visited it, and it is a link because `/site/guide` is a page; were `guide`
a heading only chapter, its crumb would be plain text.
Clicking `guide` takes you to `/site/guide` and keeps `macos` on the stack, so one click takes you back.
Visiting a page that is not on the stack, such as a sibling section or another project, replaces the stack with that
page's own path.

Continuing the example, you are on `/site/guide` with `macos` still on the stack, and you follow a link to its sibling
`/site/guide/linux`:

home / site / guide / **linux**

`/site/guide/linux` is not on the stack, so `macos` is dropped and `linux` takes its place.
Opening another project from the menu, such as `/blog`, replaces the stack the same way:

home / **blog**

## Guide-level explanation
[guide-level-explanation]: #guide-level-explanation

The breadcrumb sits at the left of the site nav on every page.
Think of it as a trail: its crumbs spell out the route of the deepest page you have reached, and the page you are on
is one of them, highlighted.
Crumbs to the left of the highlight are above you; crumbs to the right are where you have been below you.

**Crumbs.** Each crumb is one URL segment, written as it appears in the URL. There are three kinds:

* A **link crumb** brightens on hover and takes you to the page its segments name.
* A **plain crumb** is a heading only chapter. It groups pages in the sidebar but has no page of its own, so there is
  nowhere for it to go.
* The **current crumb** is the page you are on. It is highlighted and is not a link.

**Visits.** Every way of reaching a page counts as a visit: a crumb, a sidebar link, a link inside a doc, the project
menu, the browser's back and forward buttons, or typing a URL.
A visit finds the deepest crumb that the trail and the new page's route share, then:

* if the new page is that crumb, as when you visit a parent, nothing is cleared and only the highlight moves;
* otherwise, the crumbs below the shared one are cleared and the rest of the new route is pushed in their place.

Going deeper is the second case with nothing to clear: the new route shares the whole trail, so its extra segments
are pushed onto the end.
For example, on `home / site / guide / macos`, visiting `/site/servers` shares `site`, clears `guide / macos`, and
pushes `servers`, giving `home / site / servers`.

Here is one session, with the current crumb in bold:

| Visit                                    | Trail                                    |
|------------------------------------------|------------------------------------------|
| `/`                                      | **home**                                 |
| `/site` from the project menu            | home / **site**                          |
| `/site/guide/macos` from the sidebar     | home / site / guide / **macos**          |
| `/site/guide` from its crumb             | home / site / **guide** / macos          |
| `/` from its crumb                       | **home** / site / guide / macos          |
| `/site/guide/macos` from its crumb       | home / site / guide / **macos**          |
| `/site/guide/linux` from the sidebar     | home / site / guide / **linux**          |
| `/blog` from the project menu            | home / **blog**                          |

Visiting `home` never loses your place, because `/` is a prefix of every route: from the home page, the trail still
leads back to the last doc you read.

Heading only chapters show up as plain crumbs wherever they fall. If `install` in `/site/install/macos` is a heading
only chapter, the trail reads:

home / site / install / **macos**

**One trail per tab.** Each browser tab keeps its own trail, and reloading the page keeps it.
A new tab, or arriving from a link outside the site, starts a fresh trail from the route of the page it opens.

## Reference-level explanation
[reference-level-explanation]: #reference-level-explanation

The trail lives entirely in the browser. The server keeps no trail state and gains no new routes; the feature is one
plain script, `static/js/breadcrumb.js`, loaded as a module from the `nav` template, so it runs on the home page and
on every doc page; a module runs after the page is parsed, like `defer`, and can export the functions its tests import. The error page does not render the nav, so it never runs there.

**Data model**

The trail is stored in `sessionStorage` under the key `breadcrumb`, as JSON. `home` is never stored: it is always the
first crumb and always a page. The first stored crumb is the project, so a trail never spans two projects.

```js
/** One crumb below `home`: a URL segment and whether the route ending at it renders a page. */
/** @typedef {{ segment: string, isPage: boolean }} Crumb */

/** The crumbs below `home`, outermost first; `trail[0]` is the project. At most four long (project plus three
 *  `docs.toml` levels). */
/** @typedef {Crumb[]} Trail */
```

Segments are kept as they appear in `location.pathname`, percent-encoded and without slashes; the trail for
`/site/guide/macos` has the segments `["site", "guide", "macos"]`.
The current crumb is not stored; it is derived from `location.pathname` on every render, since the current page is
always the crumb whose route equals the URL.

**Functions**

```js
/** Applies the visit rule: keeps the segments the trail and route share, then pushes the rest of the route. */
function newSegments(trailSegments: string[], route: string[]): string[]

/** Pairs each segment with `isPage`, from `pages` when given, otherwise from the matching crumb of `previous`. */
function annotate(segments: string[], pages: Set<string> | null, previous: Trail): Trail

/** Reads the stored trail, applies the visit for `route`, annotates it with `pages`, writes it back, and returns it;
 *  the stored trail reads as empty when missing, malformed, or storage is unavailable, and nothing is written when
 *  storage is unavailable. */
function updateTrail(route: string[], pages: Set<string> | null): Trail

/** Replaces the breadcrumb `<ol>` with `home` plus one `<li>` per crumb, marking the crumb at depth `depth` current. */
function render(list: HTMLOListElement, trail: Trail, depth: number): void

/** Splits the current URL into its route, without empty segments, and reads the sidebar's page routes, then runs one
 *  visit for them and renders the trail `updateTrail` returns. */
function onNavigate(): void
```

`newSegments` and `annotate` are pure and exported, so the visit rule and the page lookup are tested without a DOM.
`onNavigate` is the only function that reads the page: `location`, the sidebar, and the breadcrumb `<ol>`.
`updateTrail` takes `route` and `pages` rather than reading `location` and the sidebar itself: `render` needs the
route's length, and it keeps `updateTrail` testable with a stubbed `sessionStorage`.

**The visit rule**

`newSegments` finds `i`, the length of the longest common prefix of `trailSegments` and `route`.
If `i` equals `route.length`, the new page is already on the trail and `trailSegments` is returned unchanged.
Otherwise it returns `trailSegments.slice(0, i)` followed by `route.slice(i)`.

Returning to the session in the guide-level explanation:

* From `[site]`, visiting `/site/guide/macos` gives `route = [site, guide, macos]` and `i = 1`, so `guide` and `macos`
  are pushed, `guide` included although it was skipped.
* From `[site, guide, macos]`, visiting `/site/guide` gives `i = 2 = route.length`; the trail is unchanged.
* Visiting `/` gives `route = []` and `i = 0 = route.length`; the trail is unchanged. The home page therefore never
  changes the trail, only displays it.
* From `[site, guide, macos]`, visiting `/site/guide/linux` gives `i = 2`, so `macos` is cleared and `linux` pushed.
* From `[site, guide, linux]`, visiting `/blog` gives `i = 0`, so the trail becomes `[blog]`.

**Which crumbs are pages**

The doc sidebar renders every `docs.toml` node. Only nodes with a page are `<a data-route="...">` links
(`views/doc.html:32`, `:43`, `:87`); a heading only chapter is plain text with no `data-route`. The project link has
`data-route=""`. `onNavigate` collects these values into `pages`, a set of doc routes relative to the project.

The crumb at index `k` has the doc route `segments.slice(1, k + 1).join("/")`, so the project crumb's route is `""`.
On a doc page, `annotate` looks every crumb up in that set, because the sidebar always belongs to the project at
`trail[0]`. That also refreshes crumbs recorded before a push changed `docs.toml`.
On the home page there is no sidebar and `pages` is null, so `annotate` keeps each crumb's stored `isPage`.

A skipped parent is resolved by the same lookup: in the first example above, `guide` is a page if and only if the
sidebar of `/site/guide/macos` has a `[data-route="guide"]` link.

**When it runs**

`onNavigate` runs once when the script loads, then on the two htmx events `markCurrent()` already listens for:
`htmx:afterSwap`, after a sidebar or doc link swaps `#doc`, and `htmx:historyRestore`, after back or forward restores
a cached page. Both events fire after the URL has changed. A full page load (crumbs, the project menu, a typed URL,
or back and forward to a page htmx has not cached) is covered by the initial run.

**Rendering**

`render` builds the list with `textContent`, never `innerHTML`, so a segment taken from the URL is never parsed as
HTML. Labels are `decodeURIComponent` of the segment; `href`s use the segment as it appears in the URL.
It walks the trail once, appending `"/" + segment` to an `href` it carries from crumb to crumb, so each crumb's route
is built as it is rendered.
Because `updateTrail` always returns a trail that starts with the current route, the current crumb is the one at
`depth`, the route's length: `home` at depth 0, the project at depth 1.

* A link crumb is `<a href="{href}">`; it brightens on hover.
* A plain crumb is a `<span>`.
* The current crumb is a `<span aria-current="page">`, as `home` is rendered by `views/nav.html` today.

On a doc page, link crumbs below `home` carry the sidebar's `hx-get`, `hx-target="#doc"` and `hx-push-url="true"`, so
they swap `#doc` without reloading the page; `render` calls `htmx.process` on the list, since htmx binds only markup it
swapped in or saw at load. `home` is a different page with no `#doc`, so its crumb, and every crumb on the home page,
which loads no htmx, is an ordinary link.

**Corner cases**

* *Trailing slash.* `/site/guide/` and `/site/guide` give the same route, since `onNavigate` drops empty segments.
* *A removed page on the trail.* A push removes `macos` while `[site, guide, macos]` is stored. Clicking the `macos`
  crumb requests `/site/guide/macos`, which the server redirects to `/site` (`cmd/web/app/docs.go:50`). The visit for
  `/site` keeps the trail, and `annotate` finds no `macos` in the new sidebar, so it is shown as plain text.
* *A removed or renamed project.* On the home page the trail's `isPage` values are not refreshed, so a crumb into a
  project removed by a deep clean or renamed on GitHub still links. Clicking it renders the 404 page; the stale trail
  is replaced at the next doc page visit.
* *Storage unavailable.* When `sessionStorage` throws (storage disabled, some private modes), `updateTrail` reads an
  empty trail and writes nothing, so the breadcrumb shows the current page's own route with no memory.
* *A new tab.* Browsers copy `sessionStorage` into a tab opened from another tab of the site, so such a tab starts
  with the opener's trail and applies the visit rule from there. A tab opened from outside the site starts empty.
* *Width.* A trail is at most five crumbs. The `<ol>` is already `min-w-0`; each crumb truncates with an ellipsis,
  with the current crumb last to shrink.
* *First paint.* The server still renders only `home`, so until the deferred script runs the breadcrumb shows `home`
  alone.

**Interaction with other features**

* *Sidebar highlighting.* `markCurrent()` is unchanged. The breadcrumb listens to the same events independently;
  merging both into one navigation hook can come later.
* *`data-route` becomes a contract.* Both `markCurrent()` and `onNavigate` depend on the sidebar giving every page,
  and only pages, a `data-route`. The alternative is a server-rendered list of page routes, such as
  `<script type="application/json" id="doc-routes">` built from `Repo.DocSections`, which would make that contract
  explicit at the cost of duplicating what the sidebar already renders.
* *Project menu highlighting.* Changing project is always a full page load, since the menu's links have no `hx-get`.
  The menu's `aria-current` is therefore rendered by the server from the repo it is serving; this needs the `nav`
  template to receive the current repo as well as the list, and needs no script.
* *Ingestion.* No changes: crumbs are URL segments, which are `DocSection.Segment` values the sidebar already renders.
