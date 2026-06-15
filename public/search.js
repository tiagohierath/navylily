// Lessons index: prefill the header search box from ?q=, filter the lesson
// list live, and search forum posts too — results below, lessons first.
(function () {
  var mobileQ = document.getElementById('mobile-q');
  var box = (mobileQ && window.innerWidth < 640) ? mobileQ
          : (document.querySelector('header.site input[name=q]') || mobileQ);
  var items = [].slice.call(document.querySelectorAll('main li')).filter(function (li) {
    return !li.closest('#forum-results'); // don't hide forum results with the lesson filter
  });
  var wrap = document.getElementById('forum-results');
  var list = document.getElementById('forum-list');

  // Accent-insensitive ("pratica" finds "prática"); every word must hit.
  function norm(s) { return s.toLowerCase().normalize('NFD').replace(/[\u0300-\u036f]/g, ''); }
  // Full lesson text, fetched lazily on the first real query: the whole course
  // is free, so every lesson and wiki page contributes its full body via the
  // build-time /search.txt index, and search reaches inside lessons instead of
  // just titles and previews.
  var ft = null, ftReq = null;
  function withFulltext(then) {
    if (!ftReq) {
      ftReq = fetch('/search.txt')
        .then(function (r) { return r.ok ? r.text() : ''; })
        .catch(function () { return ''; })
        .then(function (text) {
          ft = {};
          text.split('\n').forEach(function (l) {
            var p = l.split('\t');
            if (p.length >= 2) ft[p[0]] = norm(p.slice(1).join(' '));
          });
        });
    }
    ftReq.then(then);
  }
  var noneMsg = null; // "Nenhuma aula encontrada." line, created on first use
  function applyLessons(q) {
    var words = norm(q.trim()).split(/\s+/).filter(Boolean);
    var any = false;
    items.forEach(function (li) {
      var a = li.querySelector('a');
      var hay = norm(li.textContent) + ' ' + ((ft && a && ft[a.getAttribute('href')]) || '');
      var show = words.every(function (w) { return hay.indexOf(w) >= 0; });
      li.style.display = show ? '' : 'none';
      if (show) any = true;
    });
    // Don't leave a section heading floating over an emptied list; when
    // nothing matches at all, say so plainly instead.
    [].slice.call(document.querySelectorAll('main ul.articles')).forEach(function (ul) {
      var has = [].slice.call(ul.children).some(function (li) { return li.style.display !== 'none'; });
      ul.style.display = has ? '' : 'none';
      var h = ul.previousElementSibling;
      while (h && h.tagName !== 'H2') h = h.previousElementSibling;
      if (h) h.style.display = has ? '' : 'none';
    });
    if (!noneMsg) {
      var firstUl = document.querySelector('main ul.articles');
      if (!firstUl) return;
      noneMsg = document.createElement('p');
      noneMsg.textContent = 'Nenhuma aula encontrada.';
      noneMsg.hidden = true;
      firstUl.parentNode.insertBefore(noneMsg, firstUl);
    }
    noneMsg.hidden = any;
  }

  var timer, lastQ = '';
  function applyForum(q) {
    q = q.trim();
    if (!wrap) return;
    if (q.length < 2) { wrap.hidden = true; list.textContent = ''; lastQ = ''; return; }
    if (q === lastQ) return;
    lastQ = q;
    clearTimeout(timer);
    timer = setTimeout(function () {
      fetch('/api/search?q=' + encodeURIComponent(q), { headers: { Accept: 'application/json' } })
        .then(function (r) { return r.ok ? r.json() : { posts: [] }; })
        .then(function (d) { renderForum(d.posts || []); })
        .catch(function () {});
    }, 250);
  }

  function renderForum(posts) {
    list.textContent = '';
    if (!posts.length) { wrap.hidden = true; return; }
    posts.forEach(function (p) {
      var li = document.createElement('li');
      var a = document.createElement('a');
      a.href = '/community?post=' + p.id;
      a.textContent = p.title || '(sem título)';
      li.appendChild(a);
      if (p.author && p.author.username) {
        var by = document.createElement('span');
        by.style.color = '#888'; by.textContent = ' · @' + p.author.username;
        li.appendChild(by);
      }
      if (p.snippet) {
        var s = document.createElement('div');
        s.style.color = '#555'; s.style.fontSize = '.9rem'; s.textContent = p.snippet;
        li.appendChild(s);
      }
      list.appendChild(li);
    });
    wrap.hidden = false;
  }

  function apply(q) {
    applyLessons(q);
    applyForum(q);
    // First real query: pull the full-text index, then filter again.
    if (q.trim() && !ft) withFulltext(function () { applyLessons(box ? box.value : q); });
  }

  var q0 = new URLSearchParams(location.search).get('q') || '';
  if (box) box.value = q0;
  apply(q0);
  if (box) {
    box.addEventListener('input', function () { apply(box.value); });
    if (box.form) box.form.addEventListener('submit', function (e) { e.preventDefault(); apply(box.value); });
  }
})();
