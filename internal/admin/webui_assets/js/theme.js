// Applies the colour scheme before the body renders, so Bootstrap's
// [data-bs-theme] variables match the OS instead of flashing light first.
// Loaded in <head> without defer for that reason; it is ~300 bytes.
(function () {
  'use strict';

  var query = window.matchMedia('(prefers-color-scheme: dark)');

  function apply() {
    document.documentElement.setAttribute('data-bs-theme', query.matches ? 'dark' : 'light');
  }

  apply();
  if (typeof query.addEventListener === 'function') {
    query.addEventListener('change', apply);
  }
})();
