// Console behaviour: toast dismissal, the ten-minute reminder snooze, and
// confirmation for destructive forms. Kept in a separate file so the pages need
// no inline script and the CSP can stay at 'self'.
(function () {
  'use strict';

  var SNOOZE_COOKIE = 'admin_snooze_initial_password';

  function setSnooze(minutes) {
    var maxAge = Math.max(1, Math.round(minutes * 60));
    var secure = window.location.protocol === 'https:' ? '; Secure' : '';
    document.cookie = SNOOZE_COOKIE + '=1; Path=/admin; Max-Age=' + maxAge +
      '; SameSite=Strict' + secure;
  }

  var stack = document.getElementById('toasts');
  if (stack) {
    function close(toast) {
      if (!toast) return;
      var minutes = Number(toast.getAttribute('data-snooze'));
      if (minutes > 0) setSnooze(minutes);
      toast.classList.add('out');
      window.setTimeout(function () { toast.remove(); }, 180);
    }

    stack.addEventListener('click', function (event) {
      if (!(event.target instanceof Element)) return;
      var button = event.target.closest('[data-dismiss]');
      if (button) close(button.closest('.toast'));
    });

    Array.prototype.forEach.call(stack.querySelectorAll('.toast[data-timeout]'), function (toast) {
      window.setTimeout(function () { close(toast); }, Number(toast.getAttribute('data-timeout')));
    });
  }

  // Destructive forms ask first. Declared here rather than with an inline
  // onsubmit attribute so 'unsafe-inline' is not needed for scripts.
  document.addEventListener('submit', function (event) {
    var form = event.target;
    var message = form.getAttribute('data-confirm');
    if (message && !window.confirm(message)) event.preventDefault();
  }, true);
})();
