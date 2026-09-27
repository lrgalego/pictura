// Voice previews: any [data-listen] button plays its URL; pressing it again,
// or another one, stops what is playing. Delegated, so buttons that htmx
// swaps in work without rebinding.
(function () {
  var audio = null, current = null;

  function settle() {
    if (current) {
      current.classList.remove('is-playing', 'is-loading');
      current.setAttribute('aria-pressed', 'false');
    }
    current = null;
  }

  function stop() {
    if (audio) {
      audio.pause();
      audio = null;
    }
    settle();
  }

  document.addEventListener('click', function (e) {
    var b = e.target.closest('[data-listen]');
    if (!b) return;
    e.preventDefault();
    var again = b === current;
    stop();
    if (again) return;
    current = b;
    b.classList.remove('is-error');
    b.classList.add('is-loading');
    var a = audio = new Audio(b.getAttribute('data-listen'));
    // Events from a clip that was stopped since must not touch the button,
    // even when the same button has started a new one.
    a.addEventListener('playing', function () {
      if (audio !== a) return;
      b.classList.remove('is-loading');
      b.classList.add('is-playing');
      b.setAttribute('aria-pressed', 'true');
    });
    a.addEventListener('ended', function () { if (audio === a) stop(); });
    a.addEventListener('error', function () {
      if (audio !== a) return;
      stop();
      b.classList.add('is-error');
      b.title = 'Could not play this voice. Press to try again.';
    });
    a.play().catch(function () {});
  });

  // A panel that closes or re-renders takes its button with it: stop.
  document.addEventListener('htmx:beforeSwap', function () {
    if (current && !document.body.contains(current)) stop();
  });
  document.addEventListener('htmx:afterSwap', function () {
    if (current && !document.body.contains(current)) stop();
  });
})();
