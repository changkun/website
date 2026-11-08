// Row selection, the right pane, keyboard, copy buttons, and the Sync now
// return address. Every page works without it: a row's title links to the
// entry's own page.
(function () {
  'use strict';

  // The header and the sync status are fixed; --hdr keeps the page below
  // them.
  var header = document.querySelector('.top');
  if (header) {
    var fit = function () { document.documentElement.style.setProperty('--hdr', header.offsetHeight + 'px'); };
    fit();
    if (window.ResizeObserver) new ResizeObserver(fit).observe(header);
  }

  // The theme button: a click is a choice, kept under the key the home
  // page and the blog share; until then the page follows the system.
  var root = document.documentElement;
  var toggle = document.querySelector('.theme-toggle');
  if (toggle) toggle.addEventListener('click', function () {
    var t = root.getAttribute('data-theme') === 'dark' ? 'light' : 'dark';
    root.setAttribute('data-theme', t);
    try { localStorage.setItem('theme', t); } catch (e) {}
  });
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', function (e) {
    var t = null;
    try { t = localStorage.getItem('theme'); } catch (err) {}
    if (t !== 'light' && t !== 'dark') root.setAttribute('data-theme', e.matches ? 'dark' : 'light');
  });

  var sync = document.querySelector('form[action="/library/sync"] input[name=next]');
  if (sync) sync.value = location.pathname + location.search;

  var pane = document.getElementById('pane');
  var wide = window.matchMedia('(min-width: 901px)');

  var tree = document.querySelector('aside.side');
  if (tree) window.addEventListener('pagehide', function () {
    try { sessionStorage.setItem('library.side-scroll', tree.scrollTop); } catch (e) {}
  });

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
    var top = header ? header.offsetHeight : 0;
    if (r.top < top + 30 || r.bottom > window.innerHeight) row.scrollIntoView({ block: 'nearest' });
    if (load) show(row);
  }

  // show loads a row's entry into the pane and puts ?e= in the address, so a
  // reload or a shared address shows the same entry.
  function show(row) {
    if (!pane || !wide.matches) return;
    if (app) app.classList.remove('no-pane');
    var u = new URL(location.href);
    u.searchParams.set('e', row.dataset.key);
    u.searchParams.delete('show');
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

  // An overlay scrollbar for the list, at its right edge, since the
  // window's own sits at the window's edge. Scrolling stays the window's.
  if (app && app.querySelector('.list')) {
    var bar = document.createElement('div');
    bar.className = 'vbar';
    bar.setAttribute('aria-hidden', 'true');
    var thumb = document.createElement('i');
    bar.appendChild(thumb);
    app.appendChild(bar);
    var fade = null;
    var geom = function () {
      var track = bar.clientHeight, view = window.innerHeight, doc = document.documentElement.scrollHeight;
      var max = doc - view;
      var h = Math.max(32, track * view / doc);
      return { track: track, max: max, h: h, room: track - h };
    };
    var place = function () {
      var g = geom();
      bar.style.display = g.max > 0 ? '' : 'none';
      if (g.max <= 0) return;
      thumb.style.height = g.h + 'px';
      thumb.style.top = (g.room * window.scrollY / g.max) + 'px';
    };
    window.addEventListener('scroll', function () {
      place();
      bar.classList.add('on');
      clearTimeout(fade);
      fade = setTimeout(function () { bar.classList.remove('on'); }, 900);
    }, { passive: true });
    window.addEventListener('resize', place);
    if (window.ResizeObserver) new ResizeObserver(place).observe(app.querySelector('.list'));
    place();
    var drag = null;
    bar.addEventListener('pointerdown', function (ev) {
      ev.preventDefault();
      var g = geom(), r = thumb.getBoundingClientRect();
      if (ev.target !== thumb) { // a click on the track jumps there
        var y = ev.clientY - bar.getBoundingClientRect().top - g.h / 2;
        window.scrollTo(0, Math.max(0, Math.min(1, y / g.room)) * g.max);
      }
      drag = { y: ev.clientY, from: window.scrollY };
      bar.setPointerCapture(ev.pointerId);
      bar.classList.add('drag');
    });
    bar.addEventListener('pointermove', function (ev) {
      if (!drag) return;
      var g = geom();
      window.scrollTo(0, drag.from + (ev.clientY - drag.y) * g.max / g.room);
    });
    var stop = function (ev) {
      if (!drag) return;
      drag = null;
      bar.releasePointerCapture(ev.pointerId);
      bar.classList.remove('drag');
    };
    bar.addEventListener('pointerup', stop);
    bar.addEventListener('pointercancel', stop);
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
    var copyLink = ev.target.closest('button.copy-link');
    if (copyLink) {
      var field = copyLink.closest('tr').querySelector('input.link-field');
      navigator.clipboard.writeText(field.value).then(function () {
        copyLink.textContent = 'Copied';
        setTimeout(function () { copyLink.textContent = 'Copy'; }, 1200);
      });
      return;
    }
    var confirmBtn = ev.target.closest('[data-confirm]');
    if (confirmBtn && !confirm(confirmBtn.dataset.confirm)) {
      ev.preventDefault();
      return;
    }
    var close = ev.target.closest('.pane .close');
    if (close && app && wide.matches) { // hide the pane without a reload
      ev.preventDefault();
      app.classList.add('no-pane');
      var rs = rows();
      if (rs[cur]) rs[cur].classList.remove('sel');
      cur = -1;
      history.replaceState(null, '', close.getAttribute('href'));
      if (sync) sync.value = location.pathname + location.search;
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

  // The filter updates the list as you type. Enter still submits the form,
  // which is also how it works without this script.
  var filter = document.querySelector('form.filter');
  if (filter) {
    var q = filter.querySelector('input[name=q]');
    var typing = null, seq = 0;
    q.addEventListener('input', function () {
      clearTimeout(typing);
      typing = setTimeout(refresh, 150);
    });
    filter.addEventListener('submit', function (ev) {
      ev.preventDefault();
      clearTimeout(typing);
      refresh();
    });
    function refresh() {
      var u = new URL(filter.getAttribute('action'), location.href);
      new FormData(filter).forEach(function (v, k) { if (v) u.searchParams.set(k, v); });
      var e = new URL(location.href).searchParams.get('e');
      if (e) u.searchParams.set('e', e);
      var n = ++seq;
      fetch(u, { credentials: 'same-origin' }).then(function (res) {
        if (!res.ok) throw new Error(res.status);
        return res.text();
      }).then(function (html) {
        if (n !== seq) return; // a later keystroke has its own request
        var doc = new DOMParser().parseFromString(html, 'text/html');
        ['form.filter .count', '.results'].forEach(function (sel) {
          var from = doc.querySelector(sel), to = document.querySelector(sel);
          if (from && to) to.replaceWith(from);
        });
        history.replaceState(null, '', u);
        if (sync) sync.value = u.pathname + u.search;
        cur = -1;
        if (e) {
          var i = rows().findIndex(function (r) { return r.dataset.key === e; });
          if (i >= 0) select(i, false);
        }
      }).catch(function () { filter.submit(); });
    }
  }

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
