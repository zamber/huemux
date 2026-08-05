// Bootstrap a directly-loaded page into the app shell.
//
// The shell (app.html) hosts lights/sync/settings as iframes so switching
// tabs is a visibility toggle rather than a navigation — that is what keeps a
// running screen-capture stream and each page's WebSocket alive across a tab
// switch. To keep URLs meaningful the shell rewrites the address bar to the
// current tab's page with replaceState, which means a reload, a restored
// WebView, or a bookmark all resolve to that page *directly* — outside the
// shell, with no frames at all.
//
// That state is not merely different, it is one-way: from a standalone page
// every nav link is a real navigation, so each tab switch reloads and
// refetches from an empty view, and there is nothing that ever puts the shell
// back. Redirecting here closes the loop.
//
// Runs before the theme snippet on purpose: there is no point resolving a
// theme for a document that is about to be replaced.
(function () {
  // Inside the shell already — this is one of the frames.
  if (window.self !== window.top) return;
  // Escape hatch: ?standalone=1 loads a page on its own, which is occasionally
  // what you want when debugging one page in isolation.
  if (location.search.indexOf('standalone') !== -1) return;

  var tab = location.pathname.indexOf('lights') !== -1 ? 'lights'
    : (location.pathname.indexOf('settings') !== -1 ? 'settings'
    : (location.pathname.indexOf('node-editor') !== -1 ? 'presets' : 'sync'));
  // replace(), not assign(): the standalone URL must not become a history
  // entry, or Back from the shell returns to the page we just redirected away
  // from and bounces straight back here.
  location.replace('/app.html#' + tab);
})();
