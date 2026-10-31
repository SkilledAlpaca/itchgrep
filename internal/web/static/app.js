// htmx discards non-2xx responses without swapping, so a 503 from a
// server with no index yet would render nothing at all. These
// responses carry a rendered explanation, so let them through while
// leaving the status code honest.
document.addEventListener('htmx:beforeSwap', function (evt) {
	var status = evt.detail.xhr.status;
	if (status === 503 || status === 400) {
		evt.detail.shouldSwap = true;
		evt.detail.isError = false;
	}
});

// The sidebar shows the top few dozen tags and carries the rest hidden
// (data-facet-tail); this narrows the whole list to what was typed, so
// a match past the head shows up too. An empty box puts the head back.
// Purely presentational - it hides and reveals list items that are
// already on the page rather than asking the server for more - so the
// sidebar stays exactly as useful with scripting off.
//
// Delegated, because the sidebar is replaced on every filter change.
document.addEventListener('input', function (e) {
	var box = e.target.closest('[data-facet-search]');
	if (!box) return;
	var needle = box.value.trim().toLowerCase();
	var list = box.parentNode.querySelectorAll('[data-facet]');
	for (var i = 0; i < list.length; i++) {
		var slug = list[i].getAttribute('data-facet');
		list[i].hidden = needle === ''
			? list[i].hasAttribute('data-facet-tail')
			: slug.indexOf(needle) === -1;
	}
});

// Spelled out as getDate/getMonth/etc rather than toLocaleString: those
// read in whatever zone the browser is already in, with no Intl call and
// no locale-dependent reordering of day/month/year to fight - which also
// keeps the result in the same "2 January 2006, 15:04" layout the server
// uses for UTC, so a converted timestamp reads as the same kind of string
// as the one it replaced rather than a foreign one.
var MONTH_NAMES = [
	'January', 'February', 'March', 'April', 'May', 'June',
	'July', 'August', 'September', 'October', 'November', 'December'
];

function pad2(n) {
	return n < 10 ? '0' + n : String(n);
}

function formatLocalDateTime(when) {
	return when.getDate() + ' ' + MONTH_NAMES[when.getMonth()] + ' ' +
		when.getFullYear() + ', ' + pad2(when.getHours()) + ':' + pad2(when.getMinutes());
}

// suffixUTC appends " UTC" to a server-rendered string unless it is there
// already - CountingSince always carries it, but nothing stops a future
// caller passing in text that already does.
function suffixUTC(text) {
	return /\bUTC$/.test(text) ? text : text + ' UTC';
}

// The server renders exact times in UTC because it has no way of knowing
// the reader's zone - a visitor comparing "14:30 UTC" against their own
// clock has to do the arithmetic themselves. Where there is a browser to
// ask, rewrite it to local time. Two different kinds of element carry a
// server-rendered UTC time, and each needs a different conversion:
//
//   - time.js-local[datetime] is already an absolute timestamp (e.g.
//     "counting since"), so the local rendering replaces its text outright
//     - the original UTC text moves onto the title rather than being
//     discarded, since it is still the one instant nobody has to guess the
//     zone of.
//   - every other time[datetime] is a relative label ("3 days ago"): its
//     text has to stay relative, or "3 days ago" would silently become an
//     absolute local timestamp instead. Its title - already the UTC
//     absolute - gains the local absolute alongside it, so hovering shows
//     both.
//
// Both are guarded by a marker attribute so a second pass - after an
// htmx swap that did not touch a given element - leaves it alone rather
// than converting an already-local rendering as though it were still UTC.
function localiseTimestamps(root) {
	localiseAbsoluteTimestamps(root);
	localiseRelativeTimestamps(root);
}

function localiseAbsoluteTimestamps(root) {
	var nodes = root.querySelectorAll('time.js-local[datetime]:not([data-localised])');
	for (var i = 0; i < nodes.length; i++) {
		var when = new Date(nodes[i].getAttribute('datetime'));
		if (isNaN(when.getTime())) continue;
		nodes[i].setAttribute('data-localised', '');
		var original = nodes[i].textContent;
		nodes[i].textContent = formatLocalDateTime(when);
		nodes[i].title = suffixUTC(original);
	}
}

function localiseRelativeTimestamps(root) {
	var nodes = root.querySelectorAll('time:not(.js-local)[datetime]:not([data-localised])');
	for (var i = 0; i < nodes.length; i++) {
		var when = new Date(nodes[i].getAttribute('datetime'));
		if (isNaN(when.getTime())) continue;
		nodes[i].setAttribute('data-localised', '');
		var originalTitle = nodes[i].getAttribute('title') || '';
		var local = formatLocalDateTime(when);
		nodes[i].title = originalTitle ? local + ' (' + originalTitle + ')' : local;
	}
}

// "/" focuses the search box, as in every other search tool. Ignored while
// typing anywhere text could be entered - an input, a textarea, a select
// (arrow-keying its options should not steal focus away) or the summary of an
// open <details>, since space/enter there already have a job. document.getElementById
// returns null on pages with no search box (e.g. /saved), and both branches
// below already no-op on null, so this never throws there.
//
// Escape is the answering half: it blurs the box regardless of how focus got
// there, so it is always available as an exit.
document.addEventListener('keydown', function (e) {
	var input = document.getElementById('query-input');
	if (e.key === 'Escape') {
		if (input && document.activeElement === input) input.blur();
		return;
	}
	if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey) return;
	var t = e.target;
	if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable || t.closest('summary'))) return;
	if (!input) return;
	e.preventDefault();
	input.focus();
	input.select();
});

// ------------------------------------------------------------------ saved --
//
// Bookmarking a result never reaches the server: itchgrep has no accounts and
// no per-visitor storage of its own, so the only honest place to keep a saved
// list is the browser that asked for it. See /saved and its privacy note.
//
// The key is versioned by name rather than by a schema field, because there
// is nothing to migrate yet and adding one before it is needed is a promise
// this code cannot keep.
var SAVED_KEY = 'itchgrep-saved';

// Caps the list rather than letting it grow without end - a runaway grid on
// /saved would eventually make the page itself unusable. 500 is generous for
// something added a click at a time.
var SAVED_CAP = 500;

// readSavedStore and writeSavedStore are the only two places that touch
// localStorage, so every failure mode - disabled storage, a private-browsing
// quota, a value some other script clobbered with non-JSON - is handled in
// one place instead of at every call site.
function readSavedStore() {
	try {
		var raw = window.localStorage.getItem(SAVED_KEY);
		var parsed = raw ? JSON.parse(raw) : {};
		return parsed && typeof parsed === 'object' ? parsed : {};
	} catch (err) {
		return {};
	}
}

function writeSavedStore(store) {
	try {
		window.localStorage.setItem(SAVED_KEY, JSON.stringify(store));
	} catch (err) {
		// Storage disabled, full, or blocked by the browser - the toggle
		// still updates on screen for this load, it just will not persist.
	}
}

function isGameSaved(id) {
	return Object.prototype.hasOwnProperty.call(readSavedStore(), id);
}

// saveGame adds one entry and enforces the cap by savedAt, not by object key
// order: numeric-looking ids (itch.io's are) get reordered ahead of string
// keys by the JS engine regardless of insertion order, so key order is not
// insertion order and cannot be trusted to find the oldest entry.
function saveGame(entry) {
	var store = readSavedStore();
	store[entry.id] = entry;

	var ids = Object.keys(store);
	if (ids.length > SAVED_CAP) {
		ids.sort(function (a, b) {
			return (store[a].savedAt || 0) - (store[b].savedAt || 0);
		});
		for (var i = 0; i < ids.length - SAVED_CAP; i++) {
			delete store[ids[i]];
		}
	}

	writeSavedStore(store);
}

function unsaveGame(id) {
	var store = readSavedStore();
	delete store[id];
	writeSavedStore(store);
}

// markSaveButton reflects one card's saved state onto its toggle button,
// shared by every place a button's state changes: initial render, an htmx
// swap, and a click.
function markSaveButton(btn, saved) {
	btn.classList.toggle('is-saved', saved);
	btn.setAttribute('aria-pressed', saved ? 'true' : 'false');
	btn.textContent = saved ? '★' : '☆';
	var title = btn.getAttribute('data-title');
	if (title) btn.setAttribute('aria-label', (saved ? 'Unsave ' : 'Save ') + title);
}

// applySavedState reflects localStorage onto every save button under root.
// Called on load and, per the htmx:afterSwap listener below, after every
// swap - a card that arrives via infinite scroll or a filter change carries
// no saved state of its own, and without this it would always render as
// unsaved even for an asset already on the list.
function applySavedState(root) {
	var buttons = root.querySelectorAll('[data-save-toggle]');
	for (var i = 0; i < buttons.length; i++) {
		var id = buttons[i].getAttribute('data-game-id');
		if (id) markSaveButton(buttons[i], isGameSaved(id));
	}
}

// refreshSavedEmptyState shows the "nothing saved yet" notice exactly when
// the grid has no cards left in it - needed because unsaving a card on
// /saved removes it from the grid without a reload, which renderSavedGrid's
// own empty check runs once too early to catch.
function refreshSavedEmptyState() {
	var grid = document.querySelector('[data-saved-grid]');
	var empty = document.getElementById('saved-empty');
	if (!grid || !empty) return;
	empty.hidden = grid.querySelector('.asset') !== null;
}

// renderSavedGrid is /saved's entire reason for having app.js: the server
// rendered an empty grid and a <template> because it has nothing of its own
// to fill either with, so this is the only place the saved list is ever
// turned into cards.
function renderSavedGrid() {
	var grid = document.querySelector('[data-saved-grid]');
	var template = document.getElementById('saved-card-template');
	if (!grid || !template) return;

	var store = readSavedStore();
	var entries = Object.keys(store).map(function (id) {
		return store[id];
	});
	// Newest-saved first, matching every other list on the site.
	entries.sort(function (a, b) {
		return (b.savedAt || 0) - (a.savedAt || 0);
	});

	// Re-rendered from scratch rather than diffed: this only ever runs on
	// load and after a save/unsave on this same page, never on a timer, so
	// there is no steady-state cost to buy a diff against.
	var existing = grid.querySelectorAll('.asset');
	for (var i = 0; i < existing.length; i++) {
		existing[i].remove();
	}

	for (var j = 0; j < entries.length; j++) {
		var card = template.content.firstElementChild.cloneNode(true);
		fillSavedCard(card, entries[j]);
		grid.appendChild(card);
	}

	refreshSavedEmptyState();
}

// safeHTTPURL admits only http(s) URLs into href/src attributes. Server-side
// renders get this sanitisation from templ; this is the same rule for the one
// path that renders from localStorage instead - a store that outlives bugs
// and is writable by extensions, so a javascript: or data: URL parked there
// must die here rather than become a link.
function safeHTTPURL(url) {
	return /^https?:\/\//i.test(url) ? url : '';
}

// fillSavedCard copies one stored entry onto a cloned template card. The
// button carries the same data-* attributes a search-result card's does, so
// the shared click handler below needs no special case for which page a save
// toggle came from.
function fillSavedCard(card, entry) {
	var btn = card.querySelector('[data-save-toggle]');
	btn.setAttribute('data-game-id', entry.id || '');
	btn.setAttribute('data-title', entry.title || '');
	btn.setAttribute('data-author', entry.author || '');
	btn.setAttribute('data-link', entry.link || '');
	btn.setAttribute('data-thumb', entry.thumb || '');
	btn.setAttribute('data-price', entry.price || '');
	markSaveButton(btn, true);

	var link = card.querySelector('.asset-link');
	var safeLink = safeHTTPURL(entry.link || '');
	if (link && safeLink) link.setAttribute('href', safeLink);

	var thumb = card.querySelector('[data-role="thumb"]');
	if (thumb) {
		var safeThumb = safeHTTPURL(entry.thumb || '');
		if (safeThumb) {
			thumb.setAttribute('src', safeThumb);
			thumb.setAttribute('alt', 'Thumbnail for ' + (entry.title || ''));
		} else {
			thumb.remove();
		}
	}

	var title = card.querySelector('[data-role="title"]');
	if (title) title.textContent = entry.title || '';

	var author = card.querySelector('[data-role="author"]');
	if (author) {
		if (entry.author) {
			author.textContent = entry.author;
		} else {
			author.remove();
		}
	}

	var price = card.querySelector('[data-role="price"]');
	if (price) price.textContent = entry.price || '';
}

// One delegated listener rather than one per button: cards are created,
// swapped and removed constantly (infinite scroll, filter changes, /saved's
// own re-render), and a listener attached to the button itself would have to
// be reattached every time.
document.addEventListener('click', function (e) {
	var btn = e.target.closest('[data-save-toggle]');
	if (!btn) return;
	var id = btn.getAttribute('data-game-id');
	if (!id) return;

	if (isGameSaved(id)) {
		unsaveGame(id);
		markSaveButton(btn, false);
		// On /saved itself, unsaving is also "remove from this list" - the
		// card just rendered is the only server-free record of the entry
		// that existed, so leaving it on screen after unsaving would show a
		// state localStorage no longer agrees with.
		var grid = document.querySelector('[data-saved-grid]');
		if (grid && grid.contains(btn)) {
			var card = btn.closest('.asset');
			if (card) card.remove();
			refreshSavedEmptyState();
		}
	} else {
		saveGame({
			id: id,
			title: btn.getAttribute('data-title') || '',
			author: btn.getAttribute('data-author') || '',
			link: btn.getAttribute('data-link') || '',
			thumb: btn.getAttribute('data-thumb') || '',
			price: btn.getAttribute('data-price') || '',
			savedAt: Date.now()
		});
		markSaveButton(btn, true);
	}
});

document.addEventListener('DOMContentLoaded', function () {
	localiseTimestamps(document);
	applySavedState(document);
	renderSavedGrid();
});

// htmx events bubble to document, so this covers content swapped in after
// load without needing to know which fragment carried a time or a card -
// the same swap that brings in new cards is what makes their saved state
// stale, so both are reapplied together here rather than through a second
// listener.
document.addEventListener('htmx:afterSwap', function (evt) {
	var root = evt.target || document;
	localiseTimestamps(root);
	applySavedState(root);
});
