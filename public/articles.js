// Logged-in students: tick the lessons already completed and offer a link to
// continue from the first one still open. Logged-out visitors keep the plain
// list (the /api/completed call just 401s and nothing is drawn).
(function () {
  var links = [].slice.call(document.querySelectorAll('ul.articles .article > a'));
  if (!links.length) return;
  // Device-local layer (works logged out too): study streak and ▶ on
  // started-but-unfinished lessons. "Continuar de onde parou" and the
  // saved-lessons list live on /profile now, off the lesson lists.
  function lsGet(k, d) { try { return JSON.parse(localStorage.getItem(k)) || d; } catch (e) { return d; } }
  function dkey(d) { return d.getFullYear() + '-' + ('0' + (d.getMonth() + 1)).slice(-2) + '-' + ('0' + d.getDate()).slice(-2); }
  var vis = lsGet('nl-visited', {});
  links.forEach(function (a) {
    var slug = a.getAttribute('href').replace('.html', '').replace(/^\//, '');
    var t = a.querySelector('.title');
    if (vis[slug] && t) t.textContent = '▶ ' + t.textContent;
  });

  // Streak with emoji progression — 10 tiers, each harder to reach.
  // Thresholds are cumulative streak days required.
  var TIERS = [
    [  0, '🌱'],
    [  1, '🌿'],
    [  2, '🌸'],
    [  4, '✨'],
    [  8, '🔥'],
    [ 16, '⭐'],
    [ 32, '🌙'],
    [ 63, '💎'],
    [126, '👑'],
    [251, '🐱🪷'],
    [500, '🌻'],
  ];
  function streakEmoji(n) {
    var e = TIERS[0][1];
    for (var i = 0; i < TIERS.length; i++) { if (n >= TIERS[i][0]) e = TIERS[i][1]; }
    return e;
  }
  var days = lsGet('nl-study-days', {}), d0 = new Date(), streak = 0;
  if (!days[dkey(d0)]) d0.setDate(d0.getDate() - 1);
  while (days[dkey(d0)]) { streak++; d0.setDate(d0.getDate() - 1); }
  if (streak >= 1) {
    var sb = document.createElement('p');
    sb.textContent = streakEmoji(streak) + ' ' + streak + ' dia' + (streak === 1 ? '' : 's') + ' seguidos';
    var firstUl = document.querySelector('main ul.articles');
    if (firstUl) firstUl.parentNode.insertBefore(sb, firstUl);
  }
  // Whole-course PDF download, on every page with a lesson list (home +
  // lessons index). The file is rebuilt monthly by pdf.sh under a stable URL —
  // each build replaces it in place, so this is always the newest edition. The
  // course is free, so the PDF is publicly cacheable and a month-stamp query
  // keys the CDN cache to the rebuild cadence.
  var dl = null;
  var stamp = new Date();
  var dlUl = document.querySelector('main ul.articles');
  if (dlUl) {
    dl = document.createElement('p');
    var ptMonths = ['Janeiro', 'Fevereiro', 'Março', 'Abril', 'Maio', 'Junho',
      'Julho', 'Agosto', 'Setembro', 'Outubro', 'Novembro', 'Dezembro'];
    var ptMonth = ptMonths[stamp.getMonth()];
    var dlName = 'Navylily_' + ptMonth + '_' + stamp.getFullYear();
    var dlHref = '/downloads/' + dlName + '.pdf?v=' + stamp.toISOString().slice(0, 7);
    var dlA = document.createElement('a');
    dlA.href = dlHref;
    dlA.download = dlName + '.pdf';
    dlA.textContent = '📕 Baixar o curso em PDF';
    dlA.style.cssText = 'display:block;width:100%;box-sizing:border-box;margin:.35rem 0;' +
      'padding:.5rem .9rem;background:#0B1F3A;color:#fff;text-align:center;' +
      'text-decoration:none;font:inherit;cursor:pointer;';
    dl.appendChild(dlA);
    var dh = dlUl.previousElementSibling;
    while (dh && dh.tagName !== 'H2') dh = dh.previousElementSibling;
    dlUl.parentNode.insertBefore(dl, dh || dlUl);
  }
  fetch('/api/completed', { headers: { Accept: 'application/json' } })
    .then(function (r) { return r.ok ? r.json() : null; })
    .then(function (d) {
      if (!d) return;
      var done = {};
      (d.slugs || []).forEach(function (s) { done[s] = true; });
      var doneCount = 0, next = null;
      links.forEach(function (a) {
        var slug = a.getAttribute('href').replace('.html', '').replace(/^\//, '');
        if (done[slug]) {
          doneCount++;
          a.classList.add('is-done'); // un-gray the thumbnail
          var t = a.querySelector('.title');
          // A completed lesson earns a wave, replacing the local "started" marker.
          if (t) t.textContent = '🌊 ' + t.textContent.replace('▶ ', '');
        } else if (!next) {
          next = a;
        }
      });
      if (!doneCount) return;
    })
    .catch(function () {});
})();
