const COLS = 220;
const ROWS = 26;
const ASPECT = 1.9; // monospace cells are ~2x taller than wide
const FOCUS_ROW = 31; // virtual origin row, below the visible grid
const X_SCALE = 0.6; // <1 stretches rings horizontally (flatter bands)
const RING_FREQ = 0.3; // higher = bands closer together
const RING_SHARPNESS = 5; // higher = thinner, crisper bands
const DIP_DEPTH = 20; // distance units the centre of each band is pushed down (M shape)
const DIP_WIDTH = 35; // columns; how wide the dip is
const TAPER_START = 0.3; // fraction of grid width where the right-edge fade begins
const TAPER_END = 0.95; // fraction of grid width where the bands are fully faded
const RAMP = " .-=+*#&$@";
const BAYER = [
	[0, 8, 2, 10],
	[12, 4, 14, 6],
	[3, 11, 1, 9],
	[15, 7, 13, 5],
];

const ACTIVE_MS = 1000 / 30;
const IDLE_MS = 100;
const POINTER_EASE = 0.28;
const PRESENCE_EASE = 0.18;
const WARP_RADIUS = 20;
const WARP_STRENGTH = 9;
const GLOW = 0.6;

const clamp = (v, lo, hi) => Math.min(Math.max(v, lo), hi);

// Smooth 1 -> 0 falloff across the right side of the grid, so the terminal stands out.
function rightTaper(col) {
	const t = clamp((col / COLS - TAPER_START) / (TAPER_END - TAPER_START), 0, 1);
	return 1 - t * t * (3 - 2 * t);
}

// Extra phase distance near the centre column; bends the bands into an M.
function dipOffset(col) {
	const u = (col - COLS / 2) / DIP_WIDTH;
	return DIP_DEPTH * Math.exp(-(u * u));
}

// Distance from the virtual focus to a (possibly fractional) cell, with horizontal stretch applied.
function distanceFrom(col, row) {
	return Math.hypot((col - COLS / 2) * X_SCALE, (FOCUS_ROW - row) * ASPECT);
}

// Precomputes per-cell distance, arch envelope, and dither threshold once.
function buildGrid() {
	return Array.from({ length: ROWS }, (_, row) =>
		Array.from({ length: COLS }, (_, col) => {
			const dx = col - COLS / 2;
			const dy = (FOCUS_ROW - row) * ASPECT;
			const distance = distanceFrom(col, row);
			const arch = Math.sin(Math.atan2(dy, dx)) ** 2;
			const falloff = clamp(1.15 - distance / 110, 0, 1);
			const hole = clamp((distance - 24) / 18, 0, 1);
			return {
				phase: distance + dipOffset(col),
				envelope: arch * falloff * hole * rightTaper(col) * 1.4,
				threshold: (BAYER[row % 4][col % 4] + 0.5) / 16,
			};
		}),
	);
}

// Renders one frame of text at time t, warping rings around the pointer if present.
function renderFrame(grid, t, pointer) {
	const presence = pointer ? Math.min(pointer.presence, 1) : 0;
	let out = "";

	for (let row = 0; row < ROWS; row++) {
		for (let col = 0; col < COLS; col++) {
			const { phase, envelope, threshold } = grid[row][col];
			let d = phase;
			let boost = 0;

			if (pointer) {
				const dx = col - pointer.column;
				const dy = (row - pointer.row) * ASPECT;
				const r = Math.hypot(dx, dy);
				const o = r / WARP_RADIUS;
				if (r > 0 && o < 2.5) {
					const push = WARP_STRENGTH * presence * o * Math.exp(0.5 - o * o) * Math.SQRT2;
					const wx = col - (dx / r) * push;
					const wy = row - (dy / r) * push / ASPECT;
					d = distanceFrom(wx, wy) + dipOffset(wx);
					boost = Math.exp(-(o * o)) * presence * GLOW;
				}
			}

			let v = (0.5 + 0.5 * Math.sin(d * RING_FREQ - t * 0.35)) ** RING_SHARPNESS * envelope * (1 + boost);
			if (v < 0.05) v = 0;
			v = clamp(v, 0, 1);

			const scaled = v * (RAMP.length - 1);
			const idx = Math.min(
				RAMP.length - 1,
				Math.floor(scaled) + (scaled % 1 > threshold ? 1 : 0),
			);
			out += RAMP[idx];
		}
		if (row < ROWS - 1) out += "\n"; // no trailing newline, so the <pre> height never changes
	}
	return out;
}

// Eases pointer position and presence over the container; ignores touch.
function trackPointer(container, pre) {
	let target = null; // raw pointer position relative to pre
	let x = 0;
	let y = 0;
	let presence = 0;

	const update = (e) => {
		if (e.pointerType === "touch") return;
		const rect = pre.getBoundingClientRect();
		target = { x: e.clientX - rect.left, y: e.clientY - rect.top };
		if (presence === 0) {
			x = target.x;
			y = target.y;
		}
	};
	const leave = (e) => {
		if (e.pointerType !== "touch") target = null;
	};

	container.addEventListener("pointermove", update);
	container.addEventListener("pointerenter", update);
	container.addEventListener("pointerleave", leave);

	return {
		// Advances easing one step and returns the pointer state, or null when fully faded out.
		read() {
			if (target) {
				x += (target.x - x) * POINTER_EASE;
				y += (target.y - y) * POINTER_EASE;
			}
			presence += ((target ? 1 : 0) - presence) * PRESENCE_EASE;
			if (!target && presence < 0.01) presence = 0;

			const rect = pre.getBoundingClientRect();
			if (presence <= 0 || rect.width === 0 || rect.height === 0) return null;
			return {
				column: (x / rect.width) * COLS,
				row: (y / rect.height) * ROWS,
				presence,
			};
		},
		// True while the effect needs the fast frame rate.
		active: () => target !== null || presence > 0,
		stop() {
			container.removeEventListener("pointermove", update);
			container.removeEventListener("pointerenter", update);
			container.removeEventListener("pointerleave", leave);
		},
	};
}

// Runs the rAF loop at ~30fps active / ~10fps idle, pausing when hidden or offscreen.
function startLoop(pre, grid, pointer) {
	let raf = 0;
	let last = 0;
	let origin = null;
	let visible = true;

	const cancel = () => {
		cancelAnimationFrame(raf);
		raf = 0;
	};

	const tick = (now) => {
		if (document.hidden || !visible) {
			cancel();
			return;
		}
		if (origin === null) origin = now; // continue from the t=0 frame in the HTML
		const interval = pointer.active() ? ACTIVE_MS : IDLE_MS;
		if (now - last >= interval) {
			last = now;
			pre.textContent = renderFrame(grid, (now - origin) / 1000, pointer.read());
		}
		raf = requestAnimationFrame(tick);
	};

	const resume = () => {
		if (!raf && visible && !document.hidden) raf = requestAnimationFrame(tick);
	};

	const io = new IntersectionObserver(
		([entry]) => {
			visible = entry.isIntersecting;
			visible ? resume() : cancel();
		},
		{ rootMargin: "160px 0px" },
	);
	io.observe(pre);

	const onVisibility = () => (document.hidden ? cancel() : resume());
	document.addEventListener("visibilitychange", onVisibility);

	resume();

	return () => {
		cancel();
		io.disconnect();
		document.removeEventListener("visibilitychange", onVisibility);
	};
}

// Finds .hero-art, exits on missing element or prefers-reduced-motion, wires up the loop on its parent.
function init() {
	const pre = document.querySelector(".hero-art");
	if (!pre || !pre.parentElement) return;
	if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;

	const grid = buildGrid();
	const pointer = trackPointer(pre.parentElement, pre);
	startLoop(pre, grid, pointer);
}

document.addEventListener("DOMContentLoaded", init);
