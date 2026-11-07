// Keyboard and inline expand for the entry tables, copy buttons, and the
// Sync now return address. Every page works without it.
(function () {
  'use strict';

  var sync = document.querySelector('form[action="/library/sync"] input[name=next]');
  if (sync) sync.value = location.pathname + location.search;

  document.addEventListener('click', function (ev) {
    var copy = ev.target.closest('button.copy');
    if (copy) {
      navigator.clipboard.writeText(copy.dataset.bib).then(function () {
        var t = copy.textContent;
        copy.textContent = 'Copied';
        setTimeout(function () { copy.textContent = t; }, 1200);
      });
      return;
    }
    var confirmBtn = ev.target.closest('[data-confirm]');
    if (confirmBtn && !confirm(confirmBtn.dataset.confirm)) {
      ev.preventDefault();
      return;
    }
    var row = ev.target.closest('tr.row');
    if (row && !ev.target.closest('a')) {
      select(rows().indexOf(row));
      toggle(row);
    }
  });

  document.querySelectorAll('input.link-field').forEach(function (f) {
    f.addEventListener('focus', function () { f.select(); });
  });

  function rows() { return Array.prototype.slice.call(document.querySelectorAll('table.entries tr.row')); }
  var cur = -1;

  function select(i) {
    var rs = rows();
    if (!rs.length) return;
    i = Math.max(0, Math.min(rs.length - 1, i));
    if (rs[cur]) rs[cur].classList.remove('cur');
    cur = i;
    rs[cur].classList.add('cur');
    var r = rs[cur].getBoundingClientRect();
    if (r.top < 0 || r.bottom > innerHeight) rs[cur].scrollIntoView({ block: 'nearest' });
  }

  function toggle(row) {
    var more = row.nextElementSibling;
    if (more && more.classList.contains('more')) more.hidden = !more.hidden;
  }

  document.addEventListener('keydown', function (ev) {
    if (ev.metaKey || ev.ctrlKey || ev.altKey) return;
    var t = ev.target;
    if (t.matches('input, textarea, select')) {
      if (ev.key === 'Escape') t.blur();
      return;
    }
    var rs = rows();
    switch (ev.key) {
    case '/':
      var f = document.querySelector('.filter input');
      if (f) { ev.preventDefault(); f.focus(); f.select(); }
      break;
    case 'j': select(cur + 1); break;
    case 'k': select(cur - 1); break;
    case 'Enter': if (rs[cur]) { ev.preventDefault(); toggle(rs[cur]); } break;
    case 'o': if (rs[cur]) location.href = rs[cur].dataset.href; break;
    case 'c':
      if (rs[cur]) {
        var b = rs[cur].nextElementSibling.querySelector('button.copy');
        if (b) b.click();
      }
      break;
    }
  });
})();
