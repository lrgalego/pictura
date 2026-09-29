// Sound studio niceties, all delegated so they survive htmx morphs:
// a suggestion fills the direction box, hovering or focusing a cue lights
// its balloon on the page (and a marker scrolls to its cue), and only one
// "Direct…" box is open at a time.
(function () {
  function light(id, on) {
    document.querySelectorAll('[data-outline="' + id + '"], [data-marker="' + id + '"], [data-cue="' + id + '"]').forEach(function (el) {
      el.classList.toggle('is-lit', on);
    });
  }

  document.addEventListener('click', function (e) {
    var idea = e.target.closest('[data-suggest]');
    if (idea) {
      var box = idea.closest('form').querySelector('textarea');
      box.value = idea.dataset.suggest;
      box.focus();
      box.setSelectionRange(box.value.length, box.value.length);
      return;
    }
    var marker = e.target.closest('[data-marker]');
    if (marker) {
      e.preventDefault();
      var cue = document.querySelector('[data-cue="' + marker.dataset.marker + '"]');
      if (cue) {
        cue.scrollIntoView({ behavior: 'smooth', block: 'center' });
        cue.classList.remove('is-flash');
        void cue.offsetWidth;
        cue.classList.add('is-flash');
      }
    }
  });

  ['mouseover', 'focusin'].forEach(function (type) {
    document.addEventListener(type, function (e) {
      var el = e.target.closest('[data-cue], [data-marker]');
      if (el) light(el.dataset.cue || el.dataset.marker, true);
    });
  });
  ['mouseout', 'focusout'].forEach(function (type) {
    document.addEventListener(type, function (e) {
      var el = e.target.closest('[data-cue], [data-marker]');
      if (el && !el.contains(e.relatedTarget)) light(el.dataset.cue || el.dataset.marker, false);
    });
  });

  document.addEventListener('toggle', function (e) {
    var d = e.target;
    if (!d.matches || !d.matches('[data-direct]') || !d.open) return;
    document.querySelectorAll('[data-direct][open]').forEach(function (o) { if (o !== d) o.open = false; });
    var box = d.querySelector('textarea');
    if (box) box.focus();
  }, true);
})();
