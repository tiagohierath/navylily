// Offline lesson cache. Strategy:
//   - HTML (navigations): network first, so pages are always fresh online,
//     falling back to the last cached copy offline — a lesson you've read
//     keeps working on the train.
//   - Everything else (images, scripts): cache first — assets are versioned
//     with ?v= where they change, and the search index rides with the HTML.
//   - Auth, APIs and payment endpoints are never touched: those must always
//     hit the server, and their responses are per-session anyway.
var CACHE = 'nl-v3';

self.addEventListener('install', function () { self.skipWaiting(); });
self.addEventListener('activate', function (e) {
  e.waitUntil(caches.keys().then(function (keys) {
    return Promise.all(keys.filter(function (k) { return k !== CACHE; })
      .map(function (k) { return caches.delete(k); }));
  }).then(function () { return self.clients.claim(); }));
});

function store(req, res) {
  // Never cache a redirected response: for gated URLs (/protected/...) it
  // would pin the checkout/login page under the content's URL.
  if (res && res.ok && !res.redirected) {
    var copy = res.clone();
    caches.open(CACHE).then(function (c) { c.put(req, copy); });
  }
  return res;
}
function netFirst(req) {
  return fetch(req)
    .then(function (r) { return store(req, r); })
    .catch(function () {
      return caches.match(req).then(function (m) { return m || Response.error(); });
    });
}
function cacheFirst(req) {
  return caches.match(req).then(function (m) {
    return m || fetch(req).then(function (r) { return store(req, r); });
  });
}

self.addEventListener('fetch', function (e) {
  var req = e.request;
  if (req.method !== 'GET') return;
  var url = new URL(req.url);
  if (url.origin !== location.origin) return;
  if (/^\/(api|auth|me$|pix|card|checkout|webhooks|avatar\/me|login|signup|profile|after-login|forgot|reset|check-email)/.test(url.pathname)) return;
  // PDFs ride with the HTML rule: their URLs are stable but the files are
  // rebuilt monthly, so cache-first would pin a stale edition forever.
  var isHTML = req.mode === 'navigate' || /\.(html|pdf)$/.test(url.pathname) || /\/search\.txt$/.test(url.pathname);
  e.respondWith(isHTML ? netFirst(req) : cacheFirst(req));
});
