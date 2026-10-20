// Pictura's service worker. It makes the app installable and answers a
// page request with a friendly offline screen when there is no network.
// It caches nothing: every page and image comes fresh from the server, so
// a deploy is never hidden behind a stale copy.
const OFFLINE = `<!doctype html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<title>Offline — Pictura</title>
<style>
  :root { color-scheme: light dark; }
  body { margin: 0; min-height: 100vh; display: grid; place-items: center; font: 16px/1.5 system-ui, sans-serif;
         background: #fff9ef; color: #221b3a; text-align: center; padding: 2rem; box-sizing: border-box; }
  @media (prefers-color-scheme: dark) { body { background: #17132b; color: #f6efe2; } }
  h1 { font-size: 1.6rem; margin: 1rem 0 .25rem; }
  p { margin: 0 0 1.5rem; opacity: .8; }
  button { font: inherit; font-weight: 700; padding: .7rem 1.4rem; border-radius: .6rem; border: 2px solid #221b3a; background: #ffd23f; color: #221b3a; }
</style></head><body><main>
<svg width="72" height="72" viewBox="0 0 64 64" aria-hidden="true"><rect x="4" y="4" width="56" height="56" rx="12" fill="#221B3A"/><rect x="12" y="12" width="40" height="40" rx="6" fill="#FFD23F"/><path d="M20 22h24M20 32h24M20 42h14" stroke="#221B3A" stroke-width="5" stroke-linecap="round"/><circle cx="46" cy="44" r="8" fill="#FF7A3D" stroke="#221B3A" stroke-width="4"/></svg>
<h1>You're offline</h1><p>Pictura needs the internet to draw and read your stories.</p>
<button onclick="location.reload()">Try again</button>
</main></body></html>`;

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (event) => event.waitUntil(self.clients.claim()));
self.addEventListener('fetch', (event) => {
  if (event.request.mode !== 'navigate') return;
  event.respondWith(fetch(event.request).catch(() =>
    new Response(OFFLINE, { headers: { 'Content-Type': 'text/html; charset=utf-8' } })));
});
