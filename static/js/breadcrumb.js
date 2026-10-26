// The breadcrumb trail in the site nav, see docs/rfcs/0002-bread-crumbs.md.

const STORAGE_KEY = "breadcrumb";

// every crumb draws the slanted separator before it, as views/nav.html does for home
const ITEM_CLASS =
	"flex items-center gap-[6px] min-w-0 text-fg-muted before:content-[''] before:shrink-0 before:w-[2px] before:h-[18px] before:bg-border-strong before:rounded-[1px] before:rotate-[18deg] before:mx-[2px]";
// the current crumb shrinks last, so the page you are on stays readable on a narrow nav
const CURRENT_ITEM_CLASS = ITEM_CLASS + " shrink-[0.01]";
const LINK_CLASS =
	"min-w-0 truncate text-fg-muted no-underline rounded-sm transition-colors duration-[var(--dur)] ease-[var(--ease-out)] hover:text-fg focus-visible:outline-none focus-visible:[box-shadow:0_0_0_2px_var(--color-bg),0_0_0_4px_var(--color-focus)]";
const PLAIN_CLASS = "min-w-0 truncate cursor-default";
const CURRENT_CLASS = "min-w-0 truncate text-fg cursor-default";

/**
 * One crumb below `home`: a URL segment, percent-encoded as in `location.pathname`, and whether the route ending at it
 * renders a page.
 * @typedef {{ segment: string, isPage: boolean }} Crumb
 */

/**
 * The crumbs below `home`, outermost first; `trail[0]` is the project. At most four long (project plus three
 * `docs.toml` levels).
 * @typedef {Crumb[]} Trail
 */

// Decodes a percent-encoded segment, keeping it as is when it is malformed.
const decode = (segment) => {
	try {
		return decodeURIComponent(segment);
	} catch {
		return segment;
	}
};

// Applies the visit rule: keeps the segments the trail and route share, then pushes the rest of the route.
/**
 * @param {string[]} trailSegments
 * @param {string[]} route
 * @returns {string[]}
 */
export function newSegments(trailSegments, route) {
	let i = 0;
	while (i < trailSegments.length && i < route.length && trailSegments[i] === route[i]) i++;
	if (i === route.length) return trailSegments; // the new page is already on the trail
	return [...trailSegments.slice(0, i), ...route.slice(i)];
}

// Pairs each segment with `isPage`, from `pages` when given, otherwise from the matching crumb of `previous`.
/**
 * @param {string[]} segments
 * @param {Set<string> | null} pages
 * @param {Trail} previous
 * @returns {Trail}
 */
export function annotate(segments, pages, previous) {
	let docRoute = ""; // relative to the project, as the sidebar's data-route values are
	return segments.map((segment, k) => {
		// data-route holds the segments as written in docs.toml, the URL holds them percent-encoded
		if (k > 0) docRoute += (k > 1 ? "/" : "") + decode(segment);
		if (pages) return { segment, isPage: pages.has(docRoute) };
		// a crumb with no record of its own is linked: a stale link redirects to the project, a missing one strands it
		const kept = previous[k]?.segment === segment ? previous[k] : null;
		return { segment, isPage: kept ? kept.isPage : true };
	});
}

// Reads the stored trail, applies the visit for `route`, annotates it with `pages`, writes it back, and returns it;
// the stored trail reads as empty when missing, malformed, or storage is unavailable, and nothing is written when
// storage is unavailable.
/**
 * @param {string[]} route
 * @param {Set<string> | null} pages
 * @returns {Trail}
 */
export function updateTrail(route, pages) {
	/** @type {Trail} */
	let previous = [];
	try {
		const stored = JSON.parse(sessionStorage.getItem(STORAGE_KEY));
		const valid =
			Array.isArray(stored) &&
			stored.every((c) => typeof c?.segment === "string" && typeof c?.isPage === "boolean");
		if (valid) previous = stored;
	} catch {
		// storage is unavailable or holds something that is not JSON: start from an empty trail
	}

	const trail = annotate(
		newSegments(
			previous.map((c) => c.segment),
			route,
		),
		pages,
		previous,
	);
	try {
		sessionStorage.setItem(STORAGE_KEY, JSON.stringify(trail));
	} catch {
		// storage is unavailable: the trail lives for this page only
	}
	return trail;
}

// Replaces the breadcrumb `<ol>` with `home` plus one `<li>` per crumb, marking the crumb at depth `depth` current.
/**
 * @param {HTMLOListElement} list
 * @param {Trail} trail
 * @param {number} depth
 * @returns {void}
 */
function render(list, trail, depth) {
	// on a doc page, crumbs into the project swap #doc as the sidebar's links do; home is another page, so it loads in full
	const swaps = typeof htmx !== "undefined" && document.getElementById("doc") !== null;

	// built with textContent and the href property only, so a segment from the URL is never parsed as HTML
	const item = (label, href, isPage, current) => {
		const li = document.createElement("li");
		li.className = current ? CURRENT_ITEM_CLASS : ITEM_CLASS;
		let crumb;
		if (current) {
			crumb = document.createElement("span");
			crumb.setAttribute("aria-current", "page");
			crumb.className = CURRENT_CLASS;
		} else if (isPage) {
			crumb = document.createElement("a");
			crumb.href = href;
			crumb.className = LINK_CLASS;
			if (swaps && href !== "/") {
				crumb.setAttribute("hx-get", href);
				crumb.setAttribute("hx-target", "#doc");
				crumb.setAttribute("hx-push-url", "true");
			}
		} else {
			crumb = document.createElement("span");
			crumb.className = PLAIN_CLASS;
		}
		crumb.textContent = label;
		li.append(crumb);
		return li;
	};

	const items = [item("home", "/", true, depth === 0)];
	let href = "";
	trail.forEach((c, k) => {
		href += "/" + c.segment;
		items.push(item(decode(c.segment), href, c.isPage, depth === k + 1));
	});
	list.replaceChildren(...items);
	// htmx binds hx-* attributes only on markup it swapped in or saw at load, so new crumbs are bound here
	if (swaps) htmx.process(list);
}

// Splits the current URL into its route, without empty segments, and reads the sidebar's page routes, then runs one
// visit for them and renders the trail `updateTrail` returns.
/** @returns {void} */
function onNavigate() {
	const list = document.querySelector('nav[aria-label="Breadcrumb"] ol');
	if (!list) return;

	const route = location.pathname.split("/").filter(Boolean);
	// only pages get a data-route in the sidebar; the home page has no sidebar, so it has no say over isPage
	const links = document.querySelectorAll("[data-route]");
	const pages = links.length ? new Set([...links].map((l) => l.dataset.route)) : null;
	render(list, updateTrail(route, pages), route.length);
}

// tests import this module without a page, so the wiring runs only in a browser
if (typeof document !== "undefined") {
	onNavigate();
	document.body.addEventListener("htmx:afterSwap", onNavigate);
	document.body.addEventListener("htmx:historyRestore", onNavigate);
}
