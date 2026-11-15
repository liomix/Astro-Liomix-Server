// Console behaviour: toast auto-dismissal, the ten-minute reminder snooze, and
// confirmation for destructive forms. Kept in a separate file so the pages need
// no inline script and the CSP can stay at 'self'.
(function () {
  'use strict';

  var SNOOZE_COOKIE = 'admin_snooze_initial_password';
  // Matches the 200ms exit transition in app.css. Removing the node before the
  // animation ends would make the toast vanish instead of fade.
  var EXIT_MS = 200;

  function setSnooze(minutes) {
    var maxAge = Math.max(1, Math.round(minutes * 60));
    var secure = window.location.protocol === 'https:' ? '; Secure' : '';
    document.cookie = SNOOZE_COOKIE + '=1; Path=/admin; Max-Age=' + maxAge +
      '; SameSite=Strict' + secure;
  }

  var stack = document.getElementById('toasts');
  if (stack) {
    // close(toast, byOperator) — only a deliberate dismissal snoozes the
    // reminder. A toast that timed out must not silence the next page: the
    // account would still hold its initial password and nothing would say so.
    function close(toast, byOperator) {
      if (!toast || toast.classList.contains('out')) return;

      if (byOperator) {
        var minutes = Number(toast.getAttribute('data-snooze'));
        if (minutes > 0) setSnooze(minutes);
      }

      toast.classList.add('out');
      window.setTimeout(function () { toast.remove(); }, EXIT_MS);
    }

    // arm wires a toast's data-timeout, pausing while the pointer or keyboard
    // focus is inside it. A message that disappears mid-read is worse than one
    // that lingers, and hovering is the usual signal of "I am reading this".
    function arm(toast) {
      var remaining = Number(toast.getAttribute('data-timeout'));
      if (!isFinite(remaining) || remaining <= 0) return;

      var timer = 0;
      var startedAt = 0;

      function start() {
        startedAt = Date.now();
        timer = window.setTimeout(function () {
          timer = 0;
          close(toast, false);
        }, remaining);
      }

      function pause() {
        if (!timer) return;
        window.clearTimeout(timer);
        timer = 0;
        remaining = Math.max(800, remaining - (Date.now() - startedAt));
      }

      function resume() {
        if (!timer && remaining > 0) start();
      }

      toast.addEventListener('pointerenter', pause);
      toast.addEventListener('pointerleave', resume);
      toast.addEventListener('focusin', pause);
      toast.addEventListener('focusout', resume);

      start();
    }

    stack.addEventListener('click', function (event) {
      if (!(event.target instanceof Element)) return;
      var button = event.target.closest('[data-dismiss]');
      if (button) close(button.closest('.console-toast'), true);
    });

    // Escape dismisses the newest toast, which is what a keyboard user expects
    // from a message that appears without being asked for.
    document.addEventListener('keydown', function (event) {
      if (event.key !== 'Escape') return;
      var toasts = stack.querySelectorAll('.console-toast:not(.out)');
      if (toasts.length) close(toasts[toasts.length - 1], true);
    });

    Array.prototype.forEach.call(stack.querySelectorAll('.console-toast'), arm);
  }

  // The avatar menu is a <details> element, which the browser opens but never
  // closes on an outside click. Closing it here keeps it from covering the page
  // after the operator moved on.
  Array.prototype.forEach.call(document.querySelectorAll('details.avatar-menu'), function (menu) {
    document.addEventListener('click', function (event) {
      if (!menu.open) return;
      if (!(event.target instanceof Node) || menu.contains(event.target)) return;
      menu.open = false;
    });
    menu.addEventListener('keydown', function (event) {
      if (event.key === 'Escape') menu.open = false;
    });
  });

  // Batch selection. The rows carry data-bulk-id instead of a name, and the
  // list of ids is written into the form on submit. Doing it here rather than
  // with the form="..." attribute keeps the submitted set identical to what the
  // count says, and lets a selected row be tinted without a second selector.
  Array.prototype.forEach.call(document.querySelectorAll('form[data-bulk]'), function (form) {
    var boxes = document.querySelectorAll('[data-bulk-id]');
    var submit = form.querySelector('[data-bulk-submit]');
    var count = form.querySelector('[data-bulk-count]');
    var action = form.querySelector('select[name="action"]');
    var toggleAll = document.querySelector('[data-bulk-all="' + form.id + '"]');

    function checked() {
      var out = [];
      Array.prototype.forEach.call(boxes, function (box) {
        if (box.checked) out.push(box);
      });
      return out;
    }

    function sync() {
      var chosen = checked();
      if (count) count.textContent = chosen.length === 0 ? '未选中' : '已选 ' + chosen.length + ' 项';
      if (submit) submit.disabled = chosen.length === 0;
      if (toggleAll) {
        toggleAll.checked = chosen.length > 0 && chosen.length === boxes.length;
        toggleAll.indeterminate = chosen.length > 0 && chosen.length < boxes.length;
      }
      Array.prototype.forEach.call(boxes, function (box) {
        var row = box.closest('tr');
        if (row) row.classList.toggle('is-selected', box.checked);
      });
    }

    Array.prototype.forEach.call(boxes, function (box) {
      box.addEventListener('change', sync);
    });

    if (toggleAll) {
      toggleAll.addEventListener('change', function () {
        Array.prototype.forEach.call(boxes, function (box) {
          box.checked = toggleAll.checked;
        });
        sync();
      });
    }

    form.addEventListener('submit', function (event) {
      Array.prototype.forEach.call(form.querySelectorAll('input[name="ids"]'), function (input) {
        input.remove();
      });
      checked().forEach(function (box) {
        var hidden = document.createElement('input');
        hidden.type = 'hidden';
        hidden.name = 'ids';
        hidden.value = box.getAttribute('data-bulk-id');
        form.appendChild(hidden);
      });
      if (action && action.value === 'delete' &&
          !window.confirm('确定对选中的条目执行删除？该操作不可撤销。')) {
        event.preventDefault();
      }
    });

    sync();
  });

  // The account settings dialog. <dialog> handles focus trapping, the backdrop
  // and Escape by itself; this only wires the triggers.
  Array.prototype.forEach.call(document.querySelectorAll('[data-dialog-open]'), function (trigger) {
    trigger.addEventListener('click', function () {
      var menu = trigger.closest('details');
      if (menu) menu.open = false;

      var dialog = document.getElementById(trigger.getAttribute('data-dialog-open'));
      if (!dialog) return;
      if (typeof dialog.showModal === 'function') {
        dialog.showModal();
      } else {
        // Without <dialog> support, the settings are unreachable; say so
        // rather than failing silently.
        window.alert('当前浏览器不支持弹窗，请升级浏览器后重试。');
      }
    });
  });

  Array.prototype.forEach.call(document.querySelectorAll('dialog.app-dialog'), function (dialog) {
    Array.prototype.forEach.call(dialog.querySelectorAll('[data-dialog-close]'), function (button) {
      button.addEventListener('click', function () { dialog.close(); });
    });
    // A click on the backdrop lands on the dialog element itself, outside its
    // content box.
    dialog.addEventListener('click', function (event) {
      if (event.target !== dialog) return;
      var box = dialog.getBoundingClientRect();
      var inside = event.clientX >= box.left && event.clientX <= box.right &&
        event.clientY >= box.top && event.clientY <= box.bottom;
      if (!inside) dialog.close();
    });
  });

  // Destructive forms ask first. Declared here rather than with an inline
  // onsubmit attribute so 'unsafe-inline' is not needed for scripts.
  document.addEventListener('submit', function (event) {
    var form = event.target;
    var message = form.getAttribute('data-confirm');
    if (message && !window.confirm(message)) event.preventDefault();
  }, true);
})();
