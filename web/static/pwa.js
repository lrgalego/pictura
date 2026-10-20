// Registers the service worker, which makes Pictura installable on a home
// screen (Android asks for one) and shows an offline screen without network.
(function () {
  if (!('serviceWorker' in navigator)) return;
  window.addEventListener('load', function () {
    navigator.serviceWorker.register('/sw.js', { scope: '/' }).catch(function () {});
  });
})();
