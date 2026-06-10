// Header avatar — loaded on every page that has the site header. When the
// visitor is logged in it turns the "Profile" button (the <a href="/login"> in
// the header) into a small square avatar linking to their public @-page
// (/@username), falling back to /profile until they've picked a username.
// Logged-out visitors keep the plain "Profile" link, so nothing else changes.
//
// To avoid flashing the logged-out header for seconds on a slow connection, we
// paint optimistically from the JS-readable `nl_hint` cookie (written by the
// server at login and refreshed on every /me) *before* the /me request returns,
// then reconcile with /me's authoritative answer.
(function () {
  var link = document.querySelector('header.site a.btn[href="/login"]');
  if (!link) return;

  // Snapshot the logged-out look so we can put it back if /me says we're out
  // (e.g. the session expired but the hint cookie was still around).
  var loggedOut = {
    href: link.getAttribute('href'),
    html: link.innerHTML,
    style: link.getAttribute('style') || ''
  };

  function readHint() {
    var m = document.cookie.match(/(?:^|;\s*)nl_hint=([^;]+)/);
    if (!m) return null;
    try { return JSON.parse(decodeURIComponent(m[1])); } catch (e) { return null; }
  }

  function restore() {
    link.setAttribute('href', loggedOut.href);
    link.setAttribute('style', loggedOut.style);
    link.innerHTML = loggedOut.html;
    link.removeAttribute('title');
  }

  // Render the avatar/link from a display object:
  //   { username, has_avatar, avatar_ver, initial, email }
  function render(d) {
    var username = d.username || '';
    link.setAttribute('href', username ? '/@' + username : '/profile');
    link.title = username ? '@' + username : (d.email || 'Perfil');
    link.textContent = ''; // drop any prior box (or the "Profile" label)
    link.style.padding = '0';
    link.style.border = 'none';
    link.style.lineHeight = '0';

    var box = document.createElement('span');
    box.style.display = 'inline-block';
    box.style.width = '32px';
    box.style.height = '32px';
    box.style.borderRadius = '4px'; // square, just softened corners
    box.style.overflow = 'hidden';
    box.style.verticalAlign = 'middle';
    box.style.border = '1px solid #ccc';
    box.style.boxSizing = 'border-box';

    // A square with the first letter of the name/e-mail. Used when there's no
    // picture yet, and as the fallback if a stored picture fails to load
    // (setting textContent drops the broken <img> from the box).
    function setInitial() {
      var ch = d.initial || (username || d.email || '?').charAt(0);
      box.textContent = ch.toUpperCase();
      box.style.background = '#eee';
      box.style.textAlign = 'center';
      box.style.font = '600 18px/32px serif';
      box.style.color = '#333';
    }

    if (d.has_avatar) {
      var img = document.createElement('img');
      img.src = '/avatar/me?v=' + (d.avatar_ver || 0);
      img.alt = link.title;
      img.style.width = '100%';
      img.style.height = '100%';
      img.style.objectFit = 'cover';
      img.style.display = 'block';
      img.onerror = setInitial;
      box.appendChild(img);
    } else {
      setInitial();
    }
    link.appendChild(box);
  }

  // 1) Optimistic paint from the hint cookie, so a slow /me doesn't leave the
  //    logged-out "Profile/login" header showing (and clickable) for seconds.
  var hint = readHint();
  if (hint) {
    render({ username: hint.u, has_avatar: hint.a, avatar_ver: hint.v, initial: hint.i });
  }

  // 2) Reconcile with the authoritative /me. It also refreshes the hint cookie
  //    server-side, so picture/username changes propagate to the next page.
  fetch('/me', { headers: { Accept: 'application/json' } })
    .then(function (r) { return r.json(); })
    .then(function (d) {
      if (!d || !d.logged_in) { if (hint) restore(); return; } // session ended
      render({
        username: d.username, has_avatar: d.has_avatar,
        avatar_ver: d.avatar_ver, email: d.email
      });
      // Members aren't shoppers: their "Navy" button goes to the paid lessons
      // instead of the checkout (the label is the brand, so it stays).
      if (d.member) {
        var navy = document.querySelector('header.site a.btn[href="/comprar"]');
        if (navy) { navy.setAttribute('href', '/protected/'); navy.title = 'Aulas pagas'; }
      }
    })
    .catch(function () { /* offline: leave whatever we painted */ });
})();
