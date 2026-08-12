// Offline lesson cache. Strategy:
//   - Editable pages and unversioned resources are network first. Cloudflare
//     makes that freshness check fast without letting Cache Storage pin old
//     content forever.
//   - Explicitly versioned assets (?v=...) are cache first. Changing their
//     version changes the cache key, so repeat navigation stays instant.
//   - A visited resource is also kept here as an offline fallback, so lessons
//     already opened still work on the train.
//   - Auth, APIs and payment endpoints are never touched: those must always
//     hit the server, and their responses are per-session anyway.
var CACHE = 'nl-v4';

self.addEventListener('install', function () { self.skipWaiting(); });
self.addEventListener('activate', function (e) {
  var cleanup = caches.keys().then(function (keys) {
    return Promise.all(keys.filter(function (k) { return k !== CACHE; })
      .map(function (k) { return caches.delete(k); }));
  });
  var preload = self.registration.navigationPreload
    ? self.registration.navigationPreload.enable()
    : Promise.resolve();
  e.waitUntil(Promise.all([cleanup, preload])
    .then(function () { return self.clients.claim(); }));
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
function netFirst(req, preload) {
  return Promise.resolve(preload)
    .then(function (r) { return r || fetch(req); })
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
  var versioned = url.searchParams.has('v') && req.mode !== 'navigate';
  var preload = req.mode === 'navigate' ? e.preloadResponse : undefined;
  e.respondWith(versioned ? cacheFirst(req) : netFirst(req, preload));
});
