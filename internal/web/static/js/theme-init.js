// Applies the saved theme before first paint (classic script, no module
// features). The full theme logic lives in theme.js.
(function () {
  try {
    var t = window.localStorage.getItem('mail-export-viewer-theme');
    if (t === 'light' || t === 'dark') document.documentElement.dataset.theme = t;
  } catch (e) { /* storage unavailable: follow the system theme */ }
})();
