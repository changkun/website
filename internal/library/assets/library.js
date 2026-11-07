// Row selection, the right pane, keyboard, copy buttons, and the Sync now
// return address. Every page works without it: a row's title links to the
// entry's own page.
(function () {
  'use strict';

  var sync = document.querySelector('form[action="/library/sync"] input[name=next]');
  if (sync) sync.value = location.pathname + location.search;

  var pane = document.getElementById('pane');
  var wide = window.matchMedia('(min-width: 901px)');

  // On a phone the topic tree starts closed, so the list is on screen.
  var side = document.querySelector('.side-box');
  if (side && !wide.matches) side.open = false;

  function rows() { return Array.prototype.slice.call(document.querySelectorAll('table.entries tr.row')); }
  var cur = -1;
  var loading = null;

  // select marks row i and, on a wide screen, shows it in the pane.
  function select(i, load) {
    var rs = rows();
    if (!rs.length) return;
    i = Math.max(0, Math.min(rs.length - 1, i));
    if (rs[cur]) rs[cur].classList.remove('sel');
    cur = i;
    var row = rs[cur];
    row.classList.add('sel');
    if (document.activeElement && row.contains(document.activeElement)) document.activeElement.blur();
    row.querySelectorAll('td').forEach(function (td) { td.scrollLeft = 0; });
    var r = row.getBoundingClientRect();
    var box = row.closest('.list') || document.documentElement;
    var b = box.getBoundingClientRect();
    if (r.top < b.top + 30 || r.bottom > b.bottom) row.scrollIntoView({ block: 'nearest' });
    if (load) show(row);
  }

  // show loads a row's entry into the pane and puts ?e= in the address, so a
  // reload or a shared address shows the same entry.
  function show(row) {
    if (!pane || !wide.matches) return;
    var u = new URL(location.href);
    u.searchParams.set('e', row.dataset.key);
    u.searchParams.delete('done');
    u.searchParams.delete('error');
    history.replaceState(null, '', u);
    if (sync) sync.value = u.pathname + u.search;
    clearTimeout(loading);
    loading = setTimeout(function () {
      var src = row.dataset.href + '?pane=1&back=' + encodeURIComponent(u.pathname + u.search);
      fetch(src, { credentials: 'same-origin' }).then(function (res) {
        if (!res.ok) throw new Error(res.status);
        return res.text();
      }).then(function (html) {
        if (row.classList.contains('sel')) {
          pane.innerHTML = html;
          pane.scrollTop = 0;
        }
      }).catch(function () { location.href = row.dataset.href; });
    }, 80);
  }

  // The grips between the panes resize them. The widths are kept in this
  // browser; a double-click resets one.
  var app = document.querySelector('.app');
  var widths = { side: [160, 520], pane: [260, 900] };
  function store(k, v) { try { if (v) localStorage.setItem('library.' + k, v); else localStorage.removeItem('library.' + k); } catch (e) {} }
  function stored(k) { try { return localStorage.getItem('library.' + k); } catch (e) { return null; } }
  if (app) {
    ['side', 'pane'].forEach(function (k) {
      var v = stored(k);
      if (v) app.style.setProperty('--' + k + '-w', v);
    });
    app.querySelectorAll('.grip').forEach(function (g) {
      var k = g.dataset.grip;
      g.addEventListener('pointerdown', function (ev) {
        ev.preventDefault();
        g.setPointerCapture(ev.pointerId);
        g.classList.add('drag');
        app.classList.add('resizing');
      });
      g.addEventListener('pointermove', function (ev) {
        if (!g.hasPointerCapture(ev.pointerId)) return;
        var box = app.getBoundingClientRect();
        var w = k === 'side' ? ev.clientX - box.left : box.right - ev.clientX;
        w = Math.round(Math.max(widths[k][0], Math.min(widths[k][1], w, box.width - 420)));
        app.style.setProperty('--' + k + '-w', w + 'px');
      });
      g.addEventListener('pointerup', function (ev) {
        g.releasePointerCapture(ev.pointerId);
        g.classList.remove('drag');
        app.classList.remove('resizing');
        store(k, app.style.getPropertyValue('--' + k + '-w'));
      });
      g.addEventListener('dblclick', function () {
        app.style.removeProperty('--' + k + '-w');
        store(k, null);
      });
    });
  }

  // The row named by ?e= starts selected.
  var start = new URL(location.href).searchParams.get('e');
  if (start) {
    var i = rows().findIndex(function (r) { return r.dataset.key === start; });
    if (i >= 0) select(i, false);
  }

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
    if (!row || ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.button !== 0) return;
    if (!wide.matches) { // one column: open the entry's page
      if (!ev.target.closest('a')) location.href = row.dataset.href;
      return;
    }
    ev.preventDefault();
    select(rows().indexOf(row), true);
  });

  // A click on a title selects the row; it must not focus the link, which
  // would scroll the clipped title sideways.
  document.addEventListener('mousedown', function (ev) {
    if (wide.matches && ev.target.closest('tr.row a')) ev.preventDefault();
  });

  document.addEventListener('dblclick', function (ev) {
    var row = ev.target.closest('tr.row');
    if (row) location.href = row.dataset.href;
  });

  document.querySelectorAll('input.link-field').forEach(function (f) {
    f.addEventListener('focus', function () { f.select(); });
  });

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
    case 'j': case 'ArrowDown':
      if (rs.length) { ev.preventDefault(); select(cur + 1, true); }
      break;
    case 'k': case 'ArrowUp':
      if (rs.length) { ev.preventDefault(); select(cur - 1, true); }
      break;
    case 'o': case 'Enter':
      if (rs[cur]) location.href = rs[cur].dataset.href;
      break;
    case 'c':
      if (rs[cur] && rs[cur].dataset.bib) {
        navigator.clipboard.writeText(rs[cur].dataset.bib);
      }
      break;
    }
  });
})();
