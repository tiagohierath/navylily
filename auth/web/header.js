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
    box.className = 'hdr-ava'; // colors + border via the theme stylesheet below
    box.style.boxSizing = 'border-box';

    // A square with the first letter of the name/e-mail. Used when there's no
    // picture yet, and as the fallback if a stored picture fails to load
    // (setting textContent drops the broken <img> from the box).
    function setInitial() {
      var ch = d.initial || (username || d.email || '?').charAt(0);
      box.textContent = ch.toUpperCase();
      box.style.textAlign = 'center';
      box.style.font = '600 18px/32px "Source Sans 3", sans-serif';
    }

    if (d.has_avatar) {
      var img = document.createElement('img');
      img.loading = 'lazy';
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
      // Members aren't shoppers: drop their "Navy" (checkout) button — it also
      // frees header room on phones.
      if (d.member) {
        var navy = document.querySelector('header.site a.btn[href="/comprar"]');
        if (navy) navy.parentNode.removeChild(navy);
      }
    })
    .catch(function () { /* offline: leave whatever we painted */ });
})();

// Dark mode — lives here so every page with the header gets it without each
// file carrying its own stylesheet. The rules are injected after the page's
// <style>, so equal-specificity overrides win by order; inputs need !important
// to outrank the navy form-control rules that also use it.
(function () {
  var KEY = 'nl-theme';
  function saved() { try { return localStorage.getItem(KEY); } catch (e) { return null; } }
  var dark = saved() ? saved() === 'dark'
    : !!(window.matchMedia && matchMedia('(prefers-color-scheme: dark)').matches);

  var css = document.createElement('style');
  css.textContent =
    '@media(max-width:480px){.lbl-rest{display:none}}' +
    // The header avatar initial keeps its colors here (not inline) so dark
    // mode can flip them; comprar's palette lives in vars, flipped the same way.
    '.hdr-ava{background:#eee;color:#333;border:1px solid #ccc}' +
    'html.dark .hdr-ava{background:#1A2C49;color:#D9E2F0;border-color:#3D5A80}' +
    'html.dark{color-scheme:dark;--ink:#D9E2F0;--muted:#94A9C9;--line:#2C476E}' +
    'html.dark body{background:#070D17;color:#D9E2F0}' +
    'html.dark header.site{background:#0D1A2E}' +
    'html.dark header.site .btn{border-color:#3D5A80}' +
    // Prev/next lesson buttons: filled (like the light theme), not border-only,
    // so the way onward stays as visible on dark as on white.
    'html.dark nav.lesson-nav a{background:#28456B;border-color:#3D5A80;color:#fff}' +
    'html.dark main a{color:#9FC2F0}' +
    'html.dark .article a{color:inherit;background:rgba(255,255,255,.06)}' +
    'html.dark .article span.thumb{background:linear-gradient(#14233C,#1B2F4F)}' +
    'html.dark .hint,html.dark .meta .time,html.dark .meta .anon,html.dark .count,' +
      'html.dark .compose-hint,html.dark .article .preview,html.dark .readtime,' +
      'html.dark details.more summary{color:#94A9C9}' +
    'html.dark details.more summary:hover{color:#9FC2F0}' +
    'html.dark .post,html.dark .comments,html.dark #compose,html.dark details.toc,' +
      'html.dark #compose-md-preview{border-color:#2C476E}' +
    'html.dark .md pre{background:#0D1A2E}' +
    'html.dark .md blockquote{border-color:#3D5A80;color:#ADBFDA}' +
    'html.dark .md a,html.dark .linkbtn{color:#9FC2F0}' +
    'html.dark .del,html.dark .confirm .yes,html.dark .err{color:#FF9D9D}' +
    'html.dark .ok{color:#86D592}' +
    'html.dark .ava{background:#1A2C49;border-color:#3D5A80;color:#D9E2F0}' +
    'html.dark .avatar{background:#1A2C49;border-color:#3D5A80;color:#D9E2F0}' +
    // The wordmark is pure black on transparent; invert it to white on dark.
    'html.dark header.site .brand img{filter:invert(1)}' +
    'html.dark .heatmap .num{color:#D9E2F0}' +
    'html.dark .heatmap .label,html.dark .heatmap .today{color:#94A9C9}' +
    'html.dark .heatmap .divider{background:#2C476E}' +
    // Dark heatmap ladder: brighter = more (these out-specify the page's
    // .heatmap .l1..l4, so each step must be restated).
    'html.dark .heatmap .month{background:#14233C}' +
    'html.dark .heatmap .l1{background:#28456B}' +
    'html.dark .heatmap .l2{background:#3D6CAC}' +
    'html.dark .heatmap .l3{background:#6E9BD8}' +
    'html.dark .heatmap .l4{background:#A9C6F0}' +
    'html.dark a[role=button]{background:#0D1A2E;border-color:#3D5A80}' +
    'html.dark .post-img,html.dark #compose-img-preview img{border-color:#2C476E}' +
    'html.dark input,html.dark select,html.dark textarea{background:#0D1A2E !important;' +
      'color:#D9E2F0 !important;border-color:#3D5A80 !important}' +
    'html.dark input::placeholder,html.dark textarea::placeholder{color:#7E97BD !important}';
  document.head.appendChild(css);

  function paint() { document.documentElement.classList.toggle('dark', dark); }
  paint();

  // Random emoji on the Navy (⛵) nav button — picks a new one each page load.
  var navyEmojis = ['⛵','🎨','🖌️','✏️','🖊️','🗿','🎭','🦋','🌊','🐚','🏺','🔭','🎬','🪄','🧭','🗺️','⚗️','🦜','🌿','🐉','🪸','🦑','🧿','🪬','🫀','🧠','🦷','👁️','🪤','🎪','🧲','🪝','🫧','🪣','🧯','🪦','⚰️','🧬','🔬','🩻','🪬','🎏','🪭','🫁','🧶','🪡','🎑','🏮','🪔','🕯️','🔮','🪩','🩰','🪆','🎠','🌋','🗼','🏚️','🪨','🌑','🪐','☄️','🌀','🕳️','🫙','🪬','🧿','🐌','🦠','🪲','🦀','🐙','🦭','🐡','🦚','🦩','🦢','🪶','🍄','🌵','🌾','🪸'];
  var navyBtn = document.querySelector('header.site a.btn[href="/comprar"]');
  if (navyBtn) navyBtn.textContent = navyEmojis[Math.floor(Math.random() * navyEmojis.length)];

  var hd = document.querySelector('header.site');
  if (!hd) return;
  var btn = document.createElement('a');
  btn.className = 'btn'; btn.href = '#'; btn.setAttribute('role', 'button');
  function label() {
    btn.textContent = dark ? '☀️' : '🌙';
    btn.title = btn.ariaLabel = dark ? 'Tema claro' : 'Tema escuro';
  }
  label();
  btn.addEventListener('click', function (e) {
    e.preventDefault();
    dark = !dark;
    try { localStorage.setItem(KEY, dark ? 'dark' : 'light'); } catch (e2) {}
    paint(); label();
  });
  hd.appendChild(btn);
})();

// Keyboard shortcuts, site-wide: "/" focuses the search box, J/K follow the
// lesson prev/next links, Ctrl+Enter (or Cmd+Enter) submits the form of the
// textarea being typed in (community posts/comments, profile bio).
(function () {
  document.addEventListener('keydown', function (e) {
    if (e.defaultPrevented) return;
    var t = e.target;
    var typing = t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.isContentEditable);
    if (typing && t.tagName === 'TEXTAREA' && e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      if (t.form) {
        e.preventDefault();
        if (t.form.requestSubmit) t.form.requestSubmit(); else t.form.submit();
      }
      return;
    }
    if (typing || e.ctrlKey || e.metaKey || e.altKey) return;
    if (e.key === '/') {
      var s = document.querySelector('input[name=q], input[type=search]');
      if (s) { e.preventDefault(); s.focus(); s.select(); }
    } else if (e.key === 'j' || e.key === 'J') {
      var n = document.querySelector('.lesson-nav .nav-next');
      if (n) location.href = n.href;
    } else if (e.key === 'k' || e.key === 'K') {
      var p = document.querySelector('.lesson-nav .nav-prev');
      if (p) location.href = p.href;
    }
  });
})();

// Offline cache: visited lessons keep working without internet (sw.js).
if ('serviceWorker' in navigator) {
  navigator.serviceWorker.register('/sw.js').catch(function () {});
}
