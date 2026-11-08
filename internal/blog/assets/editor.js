// The editor at /blog/new: write a post or an idea in markdown, attach
// files, see it as the blog will show it, and publish it. At /blog/edit/…
// it opens a post or an idea that is there already, and saves it in place.
(function() {
  var DRAFT_KEY = 'blog-editor';
  var OLD_DRAFT_KEY = 'compose-draft'; // the ideas page's compose box, before this page
  var MAX_FILE = 25 << 20;

  var ed = document.getElementById('editor');
  var titleInput = document.getElementById('editor-title');
  var titleZhInput = document.getElementById('editor-title-zh');
  var nameInput = document.getElementById('editor-name');
  var tagBox = document.getElementById('editor-tags');
  var tagInput = document.getElementById('editor-tag');
  var bodyInput = document.getElementById('editor-body');
  var fileList = document.getElementById('editor-files');
  var picker = document.getElementById('editor-pick');
  var heading = document.getElementById('editor-heading');
  var previewTitle = document.getElementById('editor-preview-title');
  var previewBody = document.getElementById('editor-preview-body');
  var status = document.getElementById('editor-status');
  var countEl = document.getElementById('editor-count');
  var sendBtn = document.getElementById('editor-send');
  var writePane = ed.querySelector('.compose-write');
  var previewPane = previewBody.parentNode;
  var kindBtns = ed.querySelectorAll('[data-kind]');
  var modeBtns = ed.querySelectorAll('[data-mode]');
  var toolBtns = ed.querySelectorAll('[data-tool]');
  var canPublish = ed.hasAttribute('data-can-publish');

  // An edit: the page gives the entry as it is in the blog, and its draft
  // is kept apart from a new one's.
  var entryEl = document.getElementById('editor-entry');
  var entry = entryEl ? JSON.parse(entryEl.textContent) : null;
  if (entry) DRAFT_KEY += ':' + entry.path;

  var mac = /Mac|iPhone|iPad/.test(navigator.platform);
  var mod = mac ? '⌘' : 'Ctrl+';

  function isZh() {
    return document.documentElement.getAttribute('data-lang') === 'zh';
  }
  function say(en, zh) { return isZh() ? zh : en; }

  // The draft: every field, and the files uploaded for it, which stay
  // uploaded, so a draft keeps them across reloads.
  var kind = 'idea';
  var files = [];

  function load() {
    var d = null;
    try { d = JSON.parse(localStorage.getItem(DRAFT_KEY)); } catch (e) {}
    if (entry) {
      // A draft of an edit counts only while the page is as it was then.
      if (!d || d.base !== entry.base) d = entry;
    } else if (!d) {
      try {
        var old = JSON.parse(localStorage.getItem(OLD_DRAFT_KEY));
        if (old) d = { kind: 'idea', title: old.title, body: old.content };
      } catch (e) {}
    }
    if (!d) return;
    kind = entry ? entry.kind : d.kind === 'post' ? 'post' : 'idea';
    titleInput.value = d.title || '';
    titleZhInput.value = d.title_zh || '';
    nameInput.value = d.name || '';
    tags = [];
    addTags((d.tags || []).join(','));
    bodyInput.value = d.body || '';
    // The page's own files have no SHA: they are in the blog already.
    files = (d.files || []).filter(function(f) { return f && f.name && (f.sha || entry); });
  }

  function current() {
    var d = {
      kind: kind,
      title: titleInput.value.trim(),
      title_zh: titleZhInput.value.trim(),
      name: nameInput.value.trim(),
      tags: tags.concat(splitTags(tagInput.value)),
      body: bodyInput.value,
      files: files.map(function(f) { return { name: f.name, sha: f.sha, size: f.size }; })
    };
    if (entry) {
      d.path = entry.path;
      d.base = entry.base;
    }
    return d;
  }

  function save() {
    try { localStorage.setItem(DRAFT_KEY, JSON.stringify(current())); } catch (e) {}
  }

  function clear() {
    try {
      localStorage.removeItem(DRAFT_KEY);
      if (!entry) localStorage.removeItem(OLD_DRAFT_KEY);
    } catch (e) {}
  }

  // Title and text grow with what is in them, inside one scrolling pane.
  function fit(el) {
    var top = writePane.scrollTop;
    el.style.height = 'auto';
    el.style.height = el.scrollHeight + 'px';
    writePane.scrollTop = top;
  }
  function fitAll() {
    if (writePane.offsetParent === null) return;
    fit(titleInput);
    fit(bodyInput);
  }

  titleInput.addEventListener('input', function() {
    if (titleInput.value.indexOf('\n') !== -1) titleInput.value = titleInput.value.replace(/\s*\n\s*/g, ' ');
    fit(titleInput);
    changed();
  });
  titleInput.addEventListener('keydown', function(e) {
    if (e.key === 'Enter' && !e.metaKey && !e.ctrlKey && !e.isComposing) {
      e.preventDefault();
      (kind === 'post' || entry ? titleZhInput : bodyInput).focus();
    }
  });
  [titleZhInput, nameInput].forEach(function(el) { el.addEventListener('input', changed); });
  bodyInput.addEventListener('input', function() {
    highlight();
    fit(bodyInput);
    changed();
  });
  window.addEventListener('resize', fitAll);

  function changed() {
    save();
    updateCount();
    updateLabels();
    schedulePreview();
  }

  // Words, with each Chinese or Japanese character counted as one.
  var CJK = /[぀-ヿ㐀-鿿豈-﫿가-힯]/g;
  function updateCount() {
    var text = bodyInput.value;
    var n = (text.match(CJK) || []).length +
      (text.replace(CJK, ' ').match(/[A-Za-z0-9À-ɏ'’-]+/g) || []).length;
    countEl.textContent = isZh() ? n + ' 字' : n + (n === 1 ? ' word' : ' words');
  }

  // Tags, each shown as it will be once a comma, Enter or a pick from the
  // list sets it down. One the blog has already is solid; a new one, which
  // no page has yet, is dashed, so a misspelt tag shows before it is saved.
  var tags = [];
  var known = {};
  Array.prototype.forEach.call(document.querySelectorAll('#editor-tag-list option'), function(o) {
    known[o.value.toLowerCase()] = o.value;
  });

  function splitTags(s) {
    return s.split(/[,，]/).map(function(t) { return t.trim().replace(/^#/, ''); }).filter(Boolean);
  }

  function addTags(s) {
    splitTags(s).forEach(function(t) {
      t = known[t.toLowerCase()] || t;
      if (!tags.some(function(x) { return x.toLowerCase() === t.toLowerCase(); })) tags.push(t);
    });
    showTags();
  }

  function showTags() {
    Array.prototype.slice.call(tagBox.querySelectorAll('.editor-tag')).forEach(function(c) { c.remove(); });
    tags.forEach(function(t, i) {
      var chip = document.createElement('span');
      var isNew = !known[t.toLowerCase()];
      chip.className = 'editor-tag' + (isNew ? ' is-new' : '');
      chip.title = isNew ? say('A new tag: no page has it yet', '新标签：还没有页面用它') : say('A tag in use', '已有的标签');
      chip.appendChild(document.createTextNode('#' + t));
      var rm = document.createElement('button');
      rm.type = 'button';
      rm.textContent = '×';
      rm.setAttribute('aria-label', say('Remove ', '移除 ') + t);
      rm.addEventListener('click', function(e) {
        e.stopPropagation();
        tags.splice(i, 1);
        showTags();
        changed();
        tagInput.focus();
      });
      chip.appendChild(rm);
      tagBox.insertBefore(chip, tagInput);
    });
    tagInput.placeholder = tags.length ? say('Add a tag', '添加标签') : say('Tags', '标签');
  }

  function commitTag() {
    if (!tagInput.value.trim()) return;
    addTags(tagInput.value);
    tagInput.value = '';
    changed();
  }

  tagInput.addEventListener('keydown', function(e) {
    if (e.isComposing) return;
    if (e.key === 'Enter' && !e.metaKey && !e.ctrlKey) {
      e.preventDefault();
      commitTag();
    } else if (e.key === 'Backspace' && !tagInput.value && tags.length) {
      e.preventDefault();
      tagInput.value = tags.pop();
      showTags();
      changed();
    }
  });
  tagInput.addEventListener('input', function(e) {
    var v = tagInput.value;
    if (/[,，]/.test(v)) {
      // What comes before the last comma is set down; the rest stays to type on.
      var cut = Math.max(v.lastIndexOf(','), v.lastIndexOf('，'));
      addTags(v.slice(0, cut));
      tagInput.value = v.slice(cut + 1).replace(/^\s+/, '');
    } else if (e.inputType === 'insertReplacementText' || (e.inputType === undefined && known[v.toLowerCase()])) {
      commitTag();
      return;
    }
    changed();
  });
  tagInput.addEventListener('blur', commitTag);
  tagBox.addEventListener('mousedown', function(e) {
    if (e.target === tagBox) {
      e.preventDefault();
      tagInput.focus();
    }
  });

  // Idea or post.
  function setKind(k) {
    kind = k;
    ed.setAttribute('data-kind', k);
    for (var i = 0; i < kindBtns.length; i++) {
      kindBtns[i].setAttribute('aria-pressed', kindBtns[i].getAttribute('data-kind') === k ? 'true' : 'false');
    }
    updateLabels();
    fitAll();
  }
  for (var k = 0; k < kindBtns.length; k++) {
    kindBtns[k].addEventListener('click', function() {
      setKind(this.getAttribute('data-kind'));
      changed();
    });
  }

  // Write and Preview. On a wide screen, both show at once.
  function setMode(mode) {
    ed.setAttribute('data-mode', mode);
    for (var i = 0; i < modeBtns.length; i++) {
      modeBtns[i].setAttribute('aria-pressed', modeBtns[i].getAttribute('data-mode') === mode ? 'true' : 'false');
    }
    if (mode === 'write') {
      fitAll();
      bodyInput.focus();
    }
    renderPreview();
  }
  for (var m = 0; m < modeBtns.length; m++) {
    modeBtns[m].addEventListener('click', function() { setMode(this.getAttribute('data-mode')); });
  }

  // Formatting. Edits go through insertText so the browser's undo keeps them.
  function insert(text) {
    bodyInput.focus();
    if (!document.execCommand('insertText', false, text)) {
      bodyInput.setRangeText(text, bodyInput.selectionStart, bodyInput.selectionEnd, 'end');
      bodyInput.dispatchEvent(new Event('input'));
    }
  }

  // Wraps the selection in marks, or unwraps it when the marks are there.
  function wrap(before, after, placeholder) {
    var v = bodyInput.value;
    var s = bodyInput.selectionStart;
    var e = bodyInput.selectionEnd;
    var sel = v.slice(s, e);
    if (v.slice(s - before.length, s) === before && v.slice(e, e + after.length) === after) {
      bodyInput.setSelectionRange(s - before.length, e + after.length);
      insert(sel);
      bodyInput.setSelectionRange(s - before.length, e - before.length);
      return;
    }
    var text = sel || placeholder;
    insert(before + text + after);
    bodyInput.setSelectionRange(s + before.length, s + before.length + text.length);
  }

  // Puts a mark at the start of every selected line, or takes it off when
  // every line has it already.
  var LINE_MARKS = {
    heading: { re: /^#{1,6}\s/, add: function() { return '## '; } },
    quote: { re: /^>\s?/, add: function() { return '> '; } },
    list: { re: /^([-*+]|\d+\.)\s/, own: /^[-*+]\s/, add: function() { return '- '; } },
    ordered: { re: /^([-*+]|\d+\.)\s/, own: /^\d+\.\s/, add: function(i) { return (i + 1) + '. '; } }
  };

  function markLines(which) {
    var mark = LINE_MARKS[which];
    var own = mark.own || mark.re;
    var v = bodyInput.value;
    var s = bodyInput.selectionStart;
    var e = bodyInput.selectionEnd;
    if (e > s && v[e - 1] === '\n') e--;
    var start = v.lastIndexOf('\n', s - 1) + 1;
    var end = v.indexOf('\n', e);
    if (end === -1) end = v.length;
    var lines = v.slice(start, end).split('\n');
    var all = lines.every(function(l) { return own.test(l); });
    var out = lines.map(function(l, i) {
      return all ? l.replace(own, '') : mark.add(i) + l.replace(mark.re, '');
    }).join('\n');
    bodyInput.setSelectionRange(start, end);
    insert(out);
    if (s === e) bodyInput.setSelectionRange(start + out.length, start + out.length);
    else bodyInput.setSelectionRange(start, start + out.length);
  }

  var TOOLS = {
    bold: function() { wrap('**', '**', say('bold', '粗体')); },
    italic: function() { wrap('*', '*', say('italic', '斜体')); },
    heading: function() { markLines('heading'); },
    quote: function() { markLines('quote'); },
    list: function() { markLines('list'); },
    ordered: function() { markLines('ordered'); },
    code: function() {
      var sel = bodyInput.value.slice(bodyInput.selectionStart, bodyInput.selectionEnd);
      if (sel.indexOf('\n') !== -1) wrap('```\n', '\n```', '');
      else wrap('`', '`', 'code');
    },
    link: function() {
      var s = bodyInput.selectionStart;
      var sel = bodyInput.value.slice(s, bodyInput.selectionEnd);
      var text = say('text', '链接');
      if (/^https?:\/\/\S+$/.test(sel)) {
        insert('[' + text + '](' + sel + ')');
        bodyInput.setSelectionRange(s + 1, s + 1 + text.length);
      } else {
        insert('[' + (sel || text) + '](https://)');
        var url = s + 1 + (sel || text).length + 2;
        bodyInput.setSelectionRange(url, url + 8);
      }
    },
    attach: function() {
      if (!canPublish) {
        status.textContent = say('Files cannot be uploaded here.', '这里不能上传文件。');
        return;
      }
      picker.click();
    }
  };

  for (var t = 0; t < toolBtns.length; t++) {
    // Keep the text's focus and selection, and a phone's keyboard, open.
    toolBtns[t].addEventListener('mousedown', function(e) { e.preventDefault(); });
    toolBtns[t].addEventListener('click', function() {
      if (bodyInput.offsetParent === null) setMode('write');
      TOOLS[this.getAttribute('data-tool')]();
    });
  }

  bodyInput.addEventListener('keydown', function(e) {
    if (!(e.metaKey || e.ctrlKey) || e.altKey || e.shiftKey) return;
    var tool = { b: 'bold', i: 'italic', k: 'link' }[e.key.toLowerCase()];
    if (tool) {
      e.preventDefault();
      TOOLS[tool]();
    }
  });

  // Files. Each is uploaded as it is attached, and the text refers to it by
  // its name, which the server makes safe and this page makes unique.
  var MEDIA = /\.(png|jpe?g|gif|webp|avif|svg|mp4|m4v|mov|webm|mp3|m4a|aac|ogg|oga|wav|flac)$/i;

  // As the server's fileName: the stem and the extension cleaned apart.
  function clean(s) {
    return s.replace(/[^A-Za-z0-9._-]+/g, '-').replace(/-{2,}/g, '-').replace(/^[-.]+|[-.]+$/g, '');
  }
  function safeName(name) {
    name = name.replace(/^.*[\\/]/, '');
    var dot = name.lastIndexOf('.');
    var stem = clean(dot >= 0 ? name.slice(0, dot) : name);
    var ext = dot >= 0 ? clean(name.slice(dot + 1)) : '';
    if (ext.length > 10) ext = '';
    if (stem.length > 80) stem = clean(stem.slice(0, 80));
    stem = stem || 'file';
    ext = ext ? '.' + ext : '';
    var taken = function(n) { return files.some(function(f) { return f.name === n; }); };
    var out = stem + ext;
    for (var i = 2; taken(out); i++) out = stem + '-' + i + ext;
    return out;
  }

  function reference(f) {
    return MEDIA.test(f.name) ? '![](' + f.name + ')' : '[' + f.name + '](' + f.name + ')';
  }

  function size(n) {
    return n >= 1 << 20 ? (n / (1 << 20)).toFixed(1) + ' MB' : Math.max(1, Math.round(n / 1024)) + ' KB';
  }

  function showFiles() {
    fileList.innerHTML = '';
    files.forEach(function(f, i) {
      var li = document.createElement('li');
      var name = document.createElement('button');
      name.type = 'button';
      name.className = 'editor-file';
      name.textContent = f.name;
      name.title = say('Insert into the text', '插入正文');
      name.addEventListener('mousedown', function(e) { e.preventDefault(); });
      name.addEventListener('click', function() { insert(reference(f)); });
      var meta = document.createElement('span');
      meta.className = 'editor-file-size';
      meta.textContent = f.size ? size(f.size) : '';
      var rm = document.createElement('button');
      rm.type = 'button';
      rm.className = 'editor-file-remove';
      rm.textContent = '×';
      rm.title = f.sha
        ? say('Remove; the text keeps any reference to it', '移除；正文中的引用需自行删除')
        : say('Delete from the page on saving; the text keeps any reference to it', '保存时从页面删除；正文中的引用需自行删除');
      rm.addEventListener('click', function() {
        files.splice(i, 1);
        showFiles();
        changed();
      });
      li.appendChild(name);
      li.appendChild(meta);
      li.appendChild(rm);
      fileList.appendChild(li);
    });
  }

  var uploading = 0;
  function attach(list) {
    Array.prototype.forEach.call(list, function(file) {
      if (file.size > MAX_FILE) {
        status.textContent = say(file.name + ' is over 25 MB.', file.name + ' 超过 25 MB。');
        return;
      }
      var name = safeName(file.name);
      var at = bodyInput.selectionEnd;
      uploading++;
      var xhr = new XMLHttpRequest();
      xhr.open('POST', '/blog/new/files?name=' + encodeURIComponent(name));
      xhr.setRequestHeader('Content-Type', 'application/octet-stream');
      xhr.upload.onprogress = function(e) {
        if (e.lengthComputable) status.textContent = say('Uploading ', '上传中 ') + name + ' ' + Math.round(100 * e.loaded / e.total) + '%';
      };
      xhr.onload = function() {
        uploading--;
        if (xhr.status !== 200) {
          status.textContent = name + ': ' + (xhr.responseText.trim() || xhr.status);
          return;
        }
        var f = JSON.parse(xhr.responseText);
        files.push(f);
        showFiles();
        status.textContent = uploading ? status.textContent : '';
        bodyInput.focus();
        bodyInput.setSelectionRange(at, at);
        var before = bodyInput.value.slice(0, at);
        insert((before && !/\n\n$/.test(before) ? (/\n$/.test(before) ? '\n' : '\n\n') : '') + reference(f) + '\n');
      };
      xhr.onerror = function() {
        uploading--;
        status.textContent = say('Uploading ' + name + ' failed.', name + ' 上传失败。');
      };
      xhr.send(file);
    });
  }

  picker.addEventListener('change', function() {
    attach(picker.files);
    picker.value = '';
  });
  ed.addEventListener('dragover', function(e) {
    if (canPublish && e.dataTransfer && Array.prototype.indexOf.call(e.dataTransfer.types, 'Files') !== -1) {
      e.preventDefault();
      ed.classList.add('dropping');
    }
  });
  ed.addEventListener('dragleave', function(e) {
    if (e.target === ed || !ed.contains(e.relatedTarget)) ed.classList.remove('dropping');
  });
  ed.addEventListener('drop', function(e) {
    ed.classList.remove('dropping');
    if (!canPublish || !e.dataTransfer || !e.dataTransfer.files.length) return;
    e.preventDefault();
    attach(e.dataTransfer.files);
  });
  bodyInput.addEventListener('paste', function(e) {
    var list = e.clipboardData && e.clipboardData.files;
    if (canPublish && list && list.length) {
      e.preventDefault();
      attach(list);
    }
  });

  // The preview is the blog's own rendering of the draft.
  var previewTimer = null;
  var previewSeq = 0;
  function schedulePreview() {
    clearTimeout(previewTimer);
    previewTimer = setTimeout(renderPreview, 300);
  }

  function renderPreview() {
    if (previewPane.offsetParent === null) return;
    var d = current();
    previewTitle.textContent = d.title;
    previewTitle.style.display = d.title ? '' : 'none';
    if (!d.body.trim()) {
      previewBody.innerHTML = '<p class="compose-preview-empty">' + say('Nothing to preview yet.', '还没有内容可以预览。') + '</p>';
      return;
    }
    var seq = ++previewSeq;
    fetch('/blog/new/preview', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(d)
    }).then(function(r) {
      if (!r.ok) throw new Error(r.status);
      return r.json();
    }).then(function(res) {
      if (seq !== previewSeq) return;
      previewBody.innerHTML = res.html;
      drawFigures();
      if (res.math && window.MathJax && MathJax.Hub) MathJax.Hub.Queue(['Typeset', MathJax.Hub, previewBody]);
      follow(0);
    }).catch(function() {
      if (seq !== previewSeq) return;
      previewBody.innerHTML = '<p class="compose-preview-empty">' + say('The preview could not load.', '预览加载失败。') + '</p>';
    });
  }

  // Publishing.
  // The text's markdown, set in colour behind the textarea. Each line is a
  // div of its own, so the preview can find where a line of the text is.
  var hl = document.getElementById('editor-hl');
  var hlLines = [];

  function esc(s) { return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;'); }
  function tag(cls, html) { return '<span class="md-' + cls + '">' + html + '</span>'; }
  function mark(s) { return tag('mark', esc(s)); }
  function math(m) { return tag('math', esc(m[0])); }

  // The markdown within a line, each rule tried where one of its first
  // characters is.
  var INLINE = [
    [/^(`+)([^`]|[^`][\s\S]*?[^`])\1(?!`)/, function(m) { return mark(m[1]) + tag('code', esc(m[2])) + mark(m[1]); }],
    [/^\{\{[%<][\s\S]*?[%>]\}\}/, function(m) { return tag('short', esc(m[0])); }],
    [/^\$\$[\s\S]+?\$\$/, math],
    [/^\$[^\s$](?:[^$]*?[^\s$\\])?\$/, math],
    [/^(!?\[)((?:[^\]\\]|\\.)*)(\]\()([^)\s]*(?:\s+"[^"]*")?)(\))/, function(m) {
      return mark(m[1]) + (m[1] === '[' ? tag('link', inline(m[2])) : inline(m[2])) + mark(m[3]) + tag('url', esc(m[4])) + mark(m[5]);
    }],
    [/^\[\^[^\]\s]+\]/, function(m) { return tag('ref', esc(m[0])); }],
    [/^<!--[\s\S]*?-->|^<\/?[A-Za-z][^<>]*>/, function(m) { return tag('html', esc(m[0])); }],
    [/^(\*\*|__)(?=\S)([\s\S]*?\S)\1/, function(m) { return mark(m[1]) + tag('strong', inline(m[2])) + mark(m[1]); }],
    [/^~~(?=\S)([\s\S]*?\S)~~/, function(m) { return mark('~~') + tag('del', inline(m[1])) + mark('~~'); }],
    [/^([*_])(?=[^\s*_])([\s\S]*?[^\s\\])\1(?![*_\w])/, function(m) { return mark(m[1]) + tag('em', inline(m[2])) + mark(m[1]); }],
    [/^\\[\\`*_{}\[\]()#+\-.!$~|<>]/, function(m) { return mark(m[0]); }]
  ];
  var STARTS = '`{$![<*_~\\';

  function inline(s) {
    var out = '', plain = '';
    for (var i = 0; i < s.length;) {
      var c = s.charAt(i), m = null, f = null;
      // _ inside a word, as in snake_case, is a letter.
      if (STARTS.indexOf(c) !== -1 && !(c === '_' && /\w/.test(s.charAt(i - 1)))) {
        var rest = s.slice(i);
        for (var r = 0; r < INLINE.length && !m; r++) {
          m = INLINE[r][0].exec(rest);
          f = INLINE[r][1];
        }
      }
      if (m) {
        out += esc(plain) + f(m);
        plain = '';
        i += m[0].length;
      } else {
        plain += c;
        i++;
      }
    }
    return out + esc(plain);
  }

  // One line, with st carrying what the lines before it opened: a fence of
  // code, a block of math, or an HTML comment.
  function lineHTML(line, st) {
    var m, t = line.trim();
    if (st.fence) {
      if (t.length >= st.fence.length && t === st.fence.charAt(0).repeat(t.length)) {
        st.fence = null;
        return mark(line);
      }
      return tag('code', esc(line));
    }
    if (st.math) {
      if (line.indexOf('$$') !== -1) st.math = false;
      return tag('math', esc(line));
    }
    if (st.comment) {
      if (line.indexOf('-->') !== -1) st.comment = false;
      return tag('html', esc(line));
    }
    if ((m = /^(\s*)(`{3,}|~{3,})(.*)$/.exec(line))) {
      st.fence = m[2];
      return esc(m[1]) + mark(m[2]) + tag('code', esc(m[3]));
    }
    if (t.indexOf('$$') === 0 && (t === '$$' || t.indexOf('$$', 2) === -1)) {
      st.math = true;
      return tag('math', esc(line));
    }
    if (t.indexOf('<!--') === 0 && t.indexOf('-->') === -1) {
      st.comment = true;
      return tag('html', esc(line));
    }
    if ((m = /^(#{1,6})(\s+)(.*)$/.exec(line))) return tag('head', mark(m[1]) + esc(m[2]) + inline(m[3]));
    if (/^\s{0,3}([-*_])(\s*\1){2,}\s*$/.test(line)) return mark(line);
    if ((m = /^(\s*(?:>\s?)+)(.*)$/.exec(line))) return mark(m[1]) + tag('quote', inline(m[2]));
    if ((m = /^(\s*)([-*+]|\d+[.)])(\s+)(\[[ xX]\]\s)?(.*)$/.exec(line))) {
      return esc(m[1]) + mark(m[2]) + esc(m[3]) + (m[4] ? mark(m[4]) : '') + inline(m[5]);
    }
    if ((m = /^(\[\^[^\]]+\]:)(.*)$/.exec(line))) return tag('ref', esc(m[1])) + inline(m[2]);
    if (/^\s*\|/.test(line)) {
      if (/^[\s|:-]+$/.test(line)) return mark(line);
      return line.split('|').map(inline).join(mark('|'));
    }
    return inline(line);
  }

  // Sets the lines again, only those that changed.
  function highlight() {
    var lines = bodyInput.value.split('\n'), st = {}, html = new Array(lines.length);
    for (var i = 0; i < lines.length; i++) html[i] = lineHTML(lines[i], st) + '\n';
    var old = hlLines, a = 0, b = 0;
    while (a < old.length && a < html.length && old[a] === html[a]) a++;
    while (b < old.length - a && b < html.length - a && old[old.length - 1 - b] === html[html.length - 1 - b]) b++;
    for (var r = old.length - b - 1; r >= a; r--) hl.removeChild(hl.children[r]);
    var frag = document.createDocumentFragment();
    for (var n = a; n < html.length - b; n++) {
      var div = document.createElement('div');
      div.innerHTML = html[n];
      frag.appendChild(div);
    }
    hl.insertBefore(frag, hl.children[a] || null);
    hlLines = html;
    pairs = null;
  }

  // The text and the preview scroll together, either one leading. The
  // preview's blocks say the line of the text they start on; between two
  // such places, each pane moves in proportion.
  var panes = [writePane, previewPane];
  var expected = [null, null]; // the scroll each pane was just given
  var pairs = null;

  function both() { return writePane.offsetParent !== null && previewPane.offsetParent !== null; }

  // The places that match, as [y in the text's pane, y in the preview],
  // each from the top of its pane's content, both rising.
  function anchors() {
    if (pairs) return pairs;
    var wTop = writePane.getBoundingClientRect().top - writePane.scrollTop;
    var pTop = previewPane.getBoundingClientRect().top - previewPane.scrollTop;
    var base = hl.getBoundingClientRect().top - wTop;
    var wMax = writePane.scrollHeight - writePane.clientHeight;
    var pMax = previewPane.scrollHeight - previewPane.clientHeight;
    var a = [[0, 0]];
    var els = previewBody.querySelectorAll('[data-line]');
    for (var i = 0; i < els.length; i++) {
      var el = els[i], row = hl.children[+el.getAttribute('data-line')];
      if (!row) continue;
      if (!el.getClientRects().length) {
        // An image a figure is drawn for stays, hidden, after the figure.
        el = el.previousElementSibling;
        if (!el || !el.classList.contains('ifig')) continue;
      }
      var s = base + row.offsetTop, p = el.getBoundingClientRect().top - pTop, last = a[a.length - 1];
      if (s > last[0] && p > last[1] && s < wMax && p < pMax) a.push([s, p]);
    }
    var end = a[a.length - 1];
    a.push([Math.max(wMax, end[0] + 1), Math.max(pMax, end[1] + 1)]);
    return (pairs = a);
  }

  // follow scrolls the other pane to where pane `from` is.
  function follow(from) {
    if (!both()) return;
    var src = panes[from], dst = panes[1 - from];
    if (expected[from] !== null) {
      var e = expected[from];
      expected[from] = null;
      if (Math.abs(src.scrollTop - e) < 1) return;
    }
    var a = anchors(), to = 1 - from, y = src.scrollTop, i = 1;
    while (i < a.length - 1 && y > a[i][from]) i++;
    var p = a[i - 1], q = a[i], t = Math.max(0, Math.min(1, (y - p[from]) / (q[from] - p[from])));
    var before = dst.scrollTop;
    dst.scrollTop = Math.round(p[to] + t * (q[to] - p[to]));
    if (dst.scrollTop !== before) expected[to] = dst.scrollTop;
  }

  writePane.addEventListener('scroll', function() { follow(0); }, { passive: true });
  previewPane.addEventListener('scroll', function() { follow(1); }, { passive: true });
  if (window.ResizeObserver) {
    var moved = new ResizeObserver(function() { pairs = null; });
    [hl, previewBody, writePane, previewPane].forEach(function(el) { moved.observe(el); });
  }

  // A post's figures, drawn in the preview by its own scripts.
  function drawFigures() {
    if (window.ifig && ifig.mount) ifig.mount(previewBody);
    pairs = null;
  }
  window.addEventListener('load', drawFigures);

  function publish() {
    var d = current();
    if (!canPublish) {
      status.textContent = say('Publishing is not configured here.', '这里不能发布。');
      return;
    }
    if (uploading) {
      status.textContent = say('Files are still uploading.', '文件仍在上传。');
      return;
    }
    if (!d.body.trim()) {
      status.textContent = say('There is nothing to publish.', '内容不能为空。');
      return;
    }
    if (!d.title && (entry || d.kind === 'post')) {
      status.textContent = entry ? say('The page needs a title.', '需要标题。') : say('A post needs a title.', '文章需要标题。');
      titleInput.focus();
      return;
    }
    sendBtn.disabled = true;
    status.textContent = entry ? say('Saving…', '保存中…') : say('Publishing…', '发布中…');
    fetch(entry ? '/blog/edit' + entry.path : '/blog/new', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(d)
    }).then(function(r) {
      if (r.status === 403) {
        location.reload(); // the session ended; the page goes to log in
        return;
      }
      if (!r.ok) {
        return r.text().then(function(msg) {
          status.textContent = msg.trim() || (entry ? say('Saving failed', '保存失败') : say('Publishing failed', '发布失败')) + ' (' + r.status + ')';
          sendBtn.disabled = false;
        });
      }
      return r.json().then(function(res) {
        clear();
        if (entry) {
          status.textContent = isZh() ? '已保存，服务器拉取后即可看到。' : res.message;
          waitForPull(res.url, res.base, 0);
          return;
        }
        status.textContent = isZh() ? (d.kind === 'post' ? '已发布，服务器拉取后即可看到。' : '想法已提交，几分钟后出现在想法页。') : res.message;
        if (d.kind === 'post') waitFor(res.url, 0);
        else setTimeout(function() { location.href = res.url; }, 4000);
      });
    }).catch(function() {
      status.textContent = say('Network error, please retry.', '网络错误，请重试。');
      sendBtn.disabled = false;
    });
  }

  // A new post shows once the server has pulled it; go there then.
  function waitFor(url, tries) {
    fetch(url, { method: 'HEAD', cache: 'no-store' }).then(function(r) {
      if (r.ok) location.href = url;
      else if (tries < 60) setTimeout(function() { waitFor(url, tries + 1); }, 3000);
    }).catch(function() {
      if (tries < 60) setTimeout(function() { waitFor(url, tries + 1); }, 3000);
    });
  }

  // An edit shows once the server has pulled it: its file is then the one
  // saved. Go to the page then.
  function waitForPull(url, base, tries) {
    var again = function() {
      if (tries < 60) setTimeout(function() { waitForPull(url, base, tries + 1); }, 3000);
    };
    fetch('/blog/edit' + entry.path + '?base', { cache: 'no-store' }).then(function(r) {
      return r.ok ? r.json() : null;
    }).then(function(res) {
      if (res && res.base === base) location.href = url;
      else again();
    }).catch(again);
  }

  sendBtn.addEventListener('click', publish);
  ed.addEventListener('keydown', function(e) {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      publish();
    }
  });

  // Bilingual placeholders and labels.
  var TOOL_LABELS = {
    bold: ['Bold', '粗体', 'B'],
    italic: ['Italic', '斜体', 'I'],
    heading: ['Heading', '标题'],
    quote: ['Quote', '引用'],
    list: ['List', '列表'],
    ordered: ['Numbered list', '编号列表'],
    code: ['Code', '代码'],
    link: ['Link', '链接', 'K'],
    attach: ['Attach files, or drop or paste them in, up to 25 MB each', '添加文件，也可拖入或粘贴，每个最多 25 MB']
  };

  function label(el, text) {
    el.setAttribute('aria-label', text);
    el.title = text;
  }

  function updateLabels() {
    var zh = isZh();
    var post = kind === 'post';
    if (entry) heading.textContent = (post ? say('Editing a post', '编辑文章') : say('Editing an idea', '编辑想法')) + ' · ' + entry.date;
    else heading.textContent = post ? say('New post', '新文章') : say('New idea', '新想法');
    document.title = heading.textContent + ' - ' + document.title.replace(/^.* - /, '');
    titleInput.placeholder = post || entry ? say('Title', '标题') : say('Title (optional)', '标题（可选）');
    titleZhInput.placeholder = say('Chinese title (optional)', '中文标题（可选）');
    nameInput.placeholder = postName(titleInput.value) || say('from-the-title', '由标题生成');
    if (entry) nameInput.title = say('The address stays as it is', '地址保持不变');
    tagInput.placeholder = tags.length ? say('Add a tag', '添加标签') : say('Tags', '标签');
    bodyInput.placeholder = post
      ? say('Write the post. Markdown, math in $…$, and {{% en %}} or {{% zh %}} blocks work.', '写文章。支持 Markdown、$…$ 数学公式，以及 {{% en %}}、{{% zh %}} 语言块。')
      : say('Write your idea. Markdown works; a model polishes and translates it.', '写下你的想法，支持 Markdown；模型会润色并翻译。');
    sendBtn.title = canPublish ? mod + 'Enter' : say('Publishing is not configured here.', '这里不能发布。');
    for (var i = 0; i < toolBtns.length; i++) {
      var l = TOOL_LABELS[toolBtns[i].getAttribute('data-tool')];
      label(toolBtns[i], l[zh ? 1 : 0] + (l[2] ? ' (' + mod + l[2] + ')' : ''));
    }
    updateCount();
  }

  // The address a post gets from its title, as the server makes it.
  function postName(s) {
    var out = '';
    var hyphen = false;
    for (var i = 0; i < s.length; i++) {
      var c = s[i];
      if (/[\p{L}\p{N}\p{M}._+~]/u.test(c)) {
        if (hyphen) out += '-';
        hyphen = false;
        out += c.toLowerCase();
      } else if (out && (c === '-' || /\s/.test(c))) {
        hyphen = true;
      }
    }
    return out.replace(/-{2,}/g, '-').replace(/^[-.]+|[-.]+$/g, '');
  }

  new MutationObserver(function() {
    updateLabels();
    renderPreview();
  }).observe(document.documentElement, { attributes: true, attributeFilter: ['data-lang'] });

  load();
  highlight();
  if (entry) {
    nameInput.value = entry.name;
    nameInput.readOnly = true;
    document.getElementById('editor-prefix').textContent = entry.prefix;
  }
  setKind(kind);
  showFiles();
  if (!canPublish) {
    sendBtn.disabled = true;
    status.textContent = say('Preview only: publishing is not configured here.', '仅预览：这里不能发布。');
  }
  setMode('write');
  // A new text goes on at its end; an edit starts at the top.
  var end = entry ? 0 : bodyInput.value.length;
  bodyInput.setSelectionRange(end, end);
  if (entry) writePane.scrollTop = 0;
})();
