// Lesson-page extras, all device-local: remember the visit (continue /
// in-progress / study streak), estimate reading time, outline long
// lessons, and bookmarking.
(function () {
  var m = /^\/((?:protected\/)?\d\d\d)\.html$/.exec(location.pathname);
  if (!m) return;
  var slug = m[1];
  var main = document.querySelector('main');
  var h1 = main && main.querySelector('h1');
  if (!main || !h1) return;
  var title = h1.textContent.trim();
  function lsGet(k, d) { try { return JSON.parse(localStorage.getItem(k)) || d; } catch (e) { return d; } }
  function lsSet(k, v) { try { localStorage.setItem(k, JSON.stringify(v)); } catch (e) {} }
  function dkey(d) { return d.getFullYear() + '-' + ('0' + (d.getMonth() + 1)).slice(-2) + '-' + ('0' + d.getDate()).slice(-2); }

  // The visit itself: continue-where-I-left-off, in-progress, study streak.
  lsSet('nl-last-lesson', { h: location.pathname, t: title });
  var vis = lsGet('nl-visited', {}); vis[slug] = 1; lsSet('nl-visited', vis);
  var days = lsGet('nl-study-days', {}); days[dkey(new Date())] = 1; lsSet('nl-study-days', days);

  // Reading time, measured before the widgets below add their own text.
  var words = (main.innerText || '').split(/\s+/).filter(Boolean).length;
  var rt = document.createElement('p');
  rt.className = 'readtime';
  rt.textContent = '🕒 cerca de ' + Math.max(1, Math.round(words / 180)) + ' min de leitura';
  h1.parentNode.insertBefore(rt, h1.nextSibling);

  // Clickable outline — only when there's actually something to outline.
  var hs = [].slice.call(main.querySelectorAll('h2')).filter(function (h) { return h.id; });
  if (hs.length >= 3) {
    var toc = document.createElement('details'); toc.className = 'toc';
    var sum = document.createElement('summary'); sum.textContent = 'Nesta aula'; toc.appendChild(sum);
    var ul = document.createElement('ul');
    hs.forEach(function (h) {
      var li = document.createElement('li'), a = document.createElement('a');
      a.href = '#' + h.id; a.textContent = h.textContent;
      li.appendChild(a); ul.appendChild(li);
    });
    toc.appendChild(ul);
    rt.parentNode.insertBefore(toc, rt.nextSibling);
  }

  // The bookmark joins the server-rendered "completar aula" widget so all
  // lesson actions share one flex row; logged-out pages (no widget) get an
  // identical row of their own, above the prev/next nav (or the course ad,
  // when a lesson has no nav).
  var row = main.querySelector('.lesson-complete');
  if (!row) {
    row = document.createElement('div'); row.className = 'nl-extras';
    main.insertBefore(row, main.querySelector('nav.lesson-nav, #navy-join'));
  }
  var bms = lsGet('nl-bookmarks', {});
  var bm = document.createElement('button'); bm.type = 'button';
  function paintBm() { bm.textContent = bms[slug] ? '★ Aula salva' : '☆ Salvar aula'; }
  bm.addEventListener('click', function () {
    if (bms[slug]) delete bms[slug]; else bms[slug] = { h: location.pathname, t: title };
    lsSet('nl-bookmarks', bms); paintBm();
  });
  paintBm();
  row.appendChild(bm);

})();
