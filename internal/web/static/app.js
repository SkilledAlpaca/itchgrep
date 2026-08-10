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

// The sidebar shows the top few dozen tags; this narrows that list to
// what was typed. Purely presentational - it hides list items that
// are already on the page rather than asking the server for more -
// so the sidebar stays exactly as useful with scripting off.
//
// Delegated, because the sidebar is replaced on every filter change.
document.addEventListener('input', function (e) {
	var box = e.target.closest('[data-facet-search]');
	if (!box) return;
	var needle = box.value.trim().toLowerCase();
	var list = box.parentNode.querySelectorAll('[data-facet]');
	for (var i = 0; i < list.length; i++) {
		var slug = list[i].getAttribute('data-facet');
		list[i].hidden = needle !== '' && slug.indexOf(needle) === -1;
	}
});

// The server renders exact times in UTC because it has no way of
// knowing the reader's zone - a visitor comparing "14:30 UTC"
// against their own clock has to do the arithmetic themselves.
// Where there is a browser to ask, rewrite it to local time. The
// UTC form stays in the markup as the no-script fallback.
function localiseTimestamps(root) {
	var nodes = root.querySelectorAll('time[datetime]:not([data-localised])');
	for (var i = 0; i < nodes.length; i++) {
		var when = new Date(nodes[i].getAttribute('datetime'));
		if (isNaN(when.getTime())) continue;
		nodes[i].setAttribute('data-localised', '');
		// Spelled out as components rather than as dateStyle plus
		// timeStyle: the spec forbids combining those with
		// timeZoneName, and doing it throws rather than degrading.
		// The zone has to be named, or a local time is
		// indistinguishable from the UTC one it replaced.
		nodes[i].title = when.toLocaleString(undefined, {
			day: 'numeric',
			month: 'long',
			year: 'numeric',
			hour: '2-digit',
			minute: '2-digit',
			timeZoneName: 'short'
		});
	}
}

document.addEventListener('DOMContentLoaded', function () {
	localiseTimestamps(document);
});

// htmx events bubble to document, so this covers content swapped in
// after load without needing to know which fragment carried a time.
document.addEventListener('htmx:afterSwap', function (evt) {
	localiseTimestamps(evt.target || document);
});

// "/" focuses the search box, as in every other search tool. Ignored
// while typing, so it does not swallow the character.
document.addEventListener('keydown', function (e) {
	if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey) return;
	var t = e.target;
	if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable)) return;
	var input = document.getElementById('query-input');
	if (!input) return;
	e.preventDefault();
	input.focus();
	input.select();
});
