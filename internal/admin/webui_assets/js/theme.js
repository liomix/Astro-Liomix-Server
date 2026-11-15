// Applies the colour scheme before the body renders, so the token swap in
// app.css matches the OS instead of flashing light first. Loaded in <head>
// without defer for exactly that reason; it is ~300 bytes.
(function () {
  'use strict';

  var query = window.matchMedia('(prefers-color-scheme: dark)');

  function apply() {
    document.documentElement.setAttribute('data-theme', query.matches ? 'dark' : 'light');
  }

  apply();
  if (typeof query.addEventListener === 'function') {
    query.addEventListener('change', apply);
  }
})();
