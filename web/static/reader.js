// Read to me: plays a story's pages line by line in each speaker's voice
// and moves a highlighter over the word being said. Everything comes from
// read.json: per page the art, and per line its clip, its box on the page
// and every word's box with the moment it is spoken.
//
// The highlight is driven by the audio clock (requestAnimationFrame reads
// currentTime), so it stays in step at any speed and after any pause.
(function () {
  var root = document.getElementById('reader');
  if (!root) return;
  function $(s) { return root.querySelector(s); }

  var img = $('.reader__img'), sheet = $('.reader__sheet'), stage = $('.reader__stage');
  var spot = $('.reader__spot'), hl = $('.reader__hl'), hotspots = $('.reader__hotspots');
  var cover = $('.reader__cover'), status = $('.reader__status'), end = $('.reader__end');
  var wait = $('.reader__wait'), waitMsg = $('.reader__wait-msg'), retryBtn = $('[data-act=retry-page]');
  var who = $('.reader__who'), pageNo = $('.reader__pageno'), notice = $('.reader__notice');
  var playBtn = $('[data-act=play]'), speedBtn = $('[data-act=speed]');
  var turnBtn = $('[data-act=turn]'), zoomBtn = $('[data-act=zoom]');

  var SPEEDS = [0.75, 1, 1.25];
  var data = null;          // read.json
  var pi = 0;               // page index
  var li = -1;              // line index on the page, -1 before the first
  var playing = false;      // audio should be running
  var wanted = false;       // the listener asked to play (waiting for a page counts)
  var rate = 1, autoTurn = true;
  var zoom = window.matchMedia('(max-width: 720px)').matches;
  var wordIdx = -1, raf = 0, poll = 0, timer = 0, prepared = false, lock = null;
  var shown = 0;            // counts show() calls: a later one wins
  var camera = '';          // the frame the zoomed view is on
  var audio = new Audio();
  audio.preload = 'auto';
  var preload = new Audio();
  preload.preload = 'auto';

  // ---------- data ----------

  function load() {
    return fetch(root.dataset.src, { headers: { Accept: 'application/json' }, credentials: 'same-origin' })
      .then(function (r) { if (!r.ok) throw new Error('HTTP ' + r.status); return r.json(); })
      .then(function (d) { data = d; update(); return d; });
  }

  function page(i) { return data && data.pages[i === undefined ? pi : i]; }
  function ready(p) { return p && p.status === 'ready'; }
  function drawn() { return data ? data.pages.filter(function (p) { return p.status !== 'undrawn'; }) : []; }

  // prepare asks the server to read and voice what is missing, once per
  // visit (a failure is retried only when the listener asks again).
  function prepare(force) {
    if (!data || data.preparing || (prepared && !force)) return;
    var need = data.pages.some(function (p) { return p.status === 'stale' || p.status === 'error'; });
    if (!need) return;
    prepared = true;
    fetch(root.dataset.prepare, { method: 'POST', credentials: 'same-origin' })
      .then(function () { schedule(800); });
  }

  function schedule(ms) {
    clearTimeout(poll);
    poll = setTimeout(function () { load().catch(function () {}).then(tick); }, ms);
  }

  function tick() {
    if (!data) return;
    var pending = data.preparing || data.pages.some(function (p) { return p.status === 'preparing'; });
    if (pending) schedule(2500);
    // Waiting for this page: go as soon as it is ready.
    if (wanted && !playing && ready(page()) && !wait.hidden) {
      hideWait();
      show(pi, false).then(function (ok) { if (ok && wanted && !playing) startPage(); });
    }
  }

  // update reflects the latest data in the cover and waiting messages.
  function update() {
    var p = page();
    var pages = drawn().length;
    notice.textContent = data.notice || '';
    notice.hidden = !data.notice;
    if (!pages) {
      status.textContent = 'No pages are drawn yet. Draw the comic first.';
      return;
    }
    if (ready(page(0))) {
      status.textContent = pages + (pages === 1 ? ' page' : ' pages') + ', ready when you are.';
    } else if (data.preparing) {
      status.textContent = 'Warming up the voices… ' + progress();
    } else if (page(0).status === 'error') {
      status.textContent = 'The voices had a hiccup. Press play to try again.';
    } else {
      status.textContent = 'Warming up the voices…';
    }
    if (!wait.hidden && p) waitMsg.textContent = waitingText(p);
    if (!shown && p && p.image) show(pi, false);
  }

  function progress() {
    return data.total ? '(' + data.progress + ' of ' + data.total + ' pages)' : '';
  }

  function waitingText(p) {
    retryBtn.hidden = p.status !== 'error';
    wait.classList.toggle('is-stuck', p.status === 'error');
    if (p.status === 'error') return 'Page ' + p.number + ' couldn\'t be read aloud.';
    if (p.status === 'undrawn') return 'Page ' + p.number + ' is not drawn yet.';
    return 'Getting page ' + p.number + ' ready… ' + progress();
  }

  // ---------- the page ----------

  function place(el, b, pad) {
    pad = pad || 0;
    el.style.left = (b.x1 * 100 - pad) + '%';
    el.style.top = (b.y1 * 100 - pad * 2 / 3) + '%';
    el.style.width = ((b.x2 - b.x1) * 100 + pad * 2) + '%';
    el.style.height = ((b.y2 - b.y1) * 100 + pad * 4 / 3) + '%';
  }

  // show puts page i on the sheet. The new art is fetched and decoded
  // before it replaces the old, and only then does the turn play: setting
  // the src straight away kept the old page on screen until the new one
  // arrived, so the turn animated the old page in again and the new one
  // popped up after it. The promise says whether page i is (still) the
  // one on the sheet once it is shown.
  function show(i, turn) {
    var p = page(i);
    if (!p) return Promise.resolve(false);
    pi = i;
    li = -1;
    var seq = ++shown;
    pageNo.textContent = 'Page ' + p.number + ' of ' + data.pages.length;
    hl.hidden = true;
    spot.hidden = true;
    who.textContent = '';
    hotspots.textContent = '';
    unzoom();
    return art(p.image).then(function () {
      if (seq !== shown) return false; // turned again meanwhile
      if (!p.image) return;
      if (img.getAttribute('src') !== p.image) {
        // Until the new src is decoded the img keeps showing the old page;
        // the turn waits for it below.
        img.src = p.image;
        img.alt = 'Page ' + p.number + (p.title ? ': ' + p.title : '');
      } else if (img.complete) {
        return;
      }
      // Swapped just now, or by an earlier show still on its way in.
      return img.decode ? img.decode().catch(function () {}) : null;
    }).then(function (ok) {
      if (ok === false || seq !== shown) return false;
      if (img.naturalWidth) sheet.style.aspectRatio = img.naturalWidth + ' / ' + img.naturalHeight;
      if (turn) {
        sheet.classList.remove('is-turning');
        void sheet.offsetWidth; // restart the animation
        sheet.classList.add('is-turning');
      }
      hotspotsFor(p);
      return true;
    });
  }

  // art fetches and decodes a page image once; the next page's is asked
  // for while this one plays, so a turn rarely waits.
  var arts = {};
  function art(src) {
    if (!src) return Promise.resolve(null);
    if (!arts[src]) {
      var pic = new Image();
      pic.src = src;
      var done = pic.decode ? pic.decode() : new Promise(function (ok, fail) { pic.onload = ok; pic.onerror = fail; });
      arts[src] = done.then(function () { return pic; }, function () { delete arts[src]; return null; });
    }
    return arts[src];
  }

  function hotspotsFor(p) {
    hotspots.textContent = '';
    (p.lines || []).forEach(function (l, k) {
      var b = document.createElement('button');
      b.type = 'button';
      b.className = 'reader__hot';
      b.setAttribute('aria-label', l.effect ? 'Sound effect: ' + l.text : (l.speaker || 'Narrator') + ': ' + l.text);
      b.title = 'Hear this again';
      place(b, l.box, 0.6);
      b.addEventListener('click', function () { wanted = true; playLine(k); });
      hotspots.appendChild(b);
    });
  }

  // span is the box around a run of words.
  function span(words) {
    var b = { x1: 1, y1: 1, x2: 0, y2: 0 };
    words.forEach(function (w) {
      b.x1 = Math.min(b.x1, w.box.x1); b.y1 = Math.min(b.y1, w.box.y1);
      b.x2 = Math.max(b.x2, w.box.x2); b.y2 = Math.max(b.y2, w.box.y2);
    });
    return b;
  }

  img.addEventListener('load', function () {
    if (img.naturalWidth && img.naturalHeight) sheet.style.aspectRatio = img.naturalWidth + ' / ' + img.naturalHeight;
  });

  // ---------- playing ----------

  function startPage() {
    var p = page();
    li = -1;
    if (page(pi + 1)) art(page(pi + 1).image); // ready for the turn
    if (!p.lines.length) {
      // A page without words: let it be looked at, then go on.
      setPlaying(true);
      timer = setTimeout(function () { setPlaying(false); endOfPage(); }, 2500 / rate);
      return;
    }
    playLine(0);
  }

  function playLine(k) {
    var p = page();
    if (!ready(p) || !p.lines[k]) return;
    clearTimeout(timer);
    li = k;
    var l = p.lines[k];
    wordIdx = -1;
    hl.hidden = true;
    place(spot, l.box, 1.2);
    spot.hidden = false;
    if (l.effect) {
      who.textContent = 'Sound effect';
      who.className = 'reader__who reader__who--effect';
    } else {
      who.textContent = l.speaker ? l.speaker + ' says' : 'The narrator';
      who.className = 'reader__who' + (l.speaker ? '' : ' reader__who--narrator');
    }
    hl.classList.toggle('is-effect', !!l.effect);
    zoomTo(frameOf(l));
    audio.src = l.audio;
    audio.playbackRate = rate;
    audio.preservesPitch = true;
    var next = p.lines[k + 1] || (ready(page(pi + 1)) && page(pi + 1).lines[0]);
    if (next) preload.src = next.audio;
    audio.play().then(function () { setPlaying(true); }).catch(function () { setPlaying(false); });
    if ('mediaSession' in navigator && window.MediaMetadata) {
      navigator.mediaSession.metadata = new MediaMetadata({ title: data.title, artist: 'Page ' + p.number });
    }
  }

  function frame() {
    raf = 0;
    if (!playing) return;
    var l = page().lines[li];
    if (l && l.effect) {
      // A sound effect lights all its words at once, for as long as it plays.
      if (wordIdx !== -2) {
        wordIdx = -2;
        place(hl, span(l.words), 0.5);
        hl.hidden = false;
        hl.classList.remove('is-pop');
        void hl.offsetWidth;
        hl.classList.add('is-pop');
      }
    } else if (l && l.words.length) {
      var t = audio.currentTime, idx = -1;
      for (var i = 0; i < l.words.length; i++) {
        if (l.words[i].start <= t + 0.04) idx = i; else break;
      }
      if (idx !== wordIdx) {
        wordIdx = idx;
        if (idx >= 0) {
          place(hl, l.words[idx].box, 0.35);
          hl.hidden = false;
          hl.classList.remove('is-pop');
          void hl.offsetWidth;
          hl.classList.add('is-pop');
        }
      }
    }
    raf = requestAnimationFrame(frame);
  }

  audio.addEventListener('ended', function () {
    var p = page();
    if (p.lines[li] && p.lines[li].words.length && !p.lines[li].effect) place(hl, p.lines[li].words[p.lines[li].words.length - 1].box, 0.35);
    // A short breath between balloons.
    timer = setTimeout(function () {
      if (li + 1 < p.lines.length) playLine(li + 1);
      else { setPlaying(false); endOfPage(); }
    }, 380 / rate);
  });
  audio.addEventListener('error', function () {
    // A clip that will not load: skip it rather than stall the story.
    if (playing) timer = setTimeout(function () {
      if (li + 1 < page().lines.length) playLine(li + 1); else { setPlaying(false); endOfPage(); }
    }, 300);
  });

  function endOfPage() {
    spot.hidden = true;
    who.textContent = '';
    if (pi + 1 >= data.pages.length || !drawn().some(function (p) { return p.number > page().number; })) {
      finish();
      return;
    }
    if (!autoTurn) { wanted = false; return; }
    timer = setTimeout(function () { turnTo(pi + 1, true); }, 900 / rate);
  }

  function turnTo(i, play) {
    if (i < 0 || i >= data.pages.length) return;
    stop();
    wanted = play;
    show(i, true).then(function (ok) {
      // The turn starts once the new page is in; it plays when it lands.
      if (!ok || !wanted) return;
      if (ready(page())) {
        timer = setTimeout(startPage, 650);
      } else {
        showWait();
      }
    });
  }

  function showWait() {
    wait.hidden = false;
    waitMsg.textContent = waitingText(page());
    if (page().status === 'stale') prepare(true);
    schedule(1500);
  }

  // retryPage asks for this one page again; what worked on it is kept.
  function retryPage() {
    var p = page();
    retryBtn.hidden = true;
    wait.classList.remove('is-stuck');
    p.status = 'preparing';
    waitMsg.textContent = 'Trying page ' + p.number + ' again…';
    fetch(root.dataset.retry + p.id + '/retry', { method: 'POST', credentials: 'same-origin' })
      .then(function () { schedule(1500); });
  }
  function hideWait() { wait.hidden = true; }

  function finish() {
    setPlaying(false);
    wanted = false;
    unzoom();
    end.hidden = false;
  }

  function stop() {
    clearTimeout(timer);
    audio.pause();
    setPlaying(false);
  }

  function setPlaying(on) {
    playing = on;
    playBtn.classList.toggle('is-playing', on);
    playBtn.setAttribute('aria-label', on ? 'Pause' : 'Play');
    if (on && !raf) raf = requestAnimationFrame(frame);
    if (on) keepAwake(); else release();
  }

  function toggle() {
    if (!cover.hidden) { begin(); return; }
    if (!end.hidden) { again(); return; }
    if (playing) { stop(); wanted = false; return; }
    wanted = true;
    if (!ready(page())) { showWait(); return; }
    if (li < 0 || !audio.src) startPage();
    else audio.play().then(function () { setPlaying(true); }).catch(function () {});
  }

  // begin is the first press: it unlocks audio on phones (the play call
  // has to happen inside the tap), hides the cover and starts page one.
  function begin() {
    audio.src = 'data:audio/wav;base64,UklGRiQAAABXQVZFZm10IBAAAAABAAEAESsAABErAAABAAgAZGF0YQAAAAA=';
    audio.play().catch(function () {});
    cover.hidden = true;
    var first = data ? data.pages.findIndex(function (p) { return p.status !== 'undrawn'; }) : 0;
    // ?page=N (the sound studio's "Play this page") starts there.
    var asked = parseInt(new URLSearchParams(location.search).get('page'), 10);
    if (data && asked > 0) {
      var at = data.pages.findIndex(function (p) { return p.number === asked; });
      if (at >= 0) first = at;
    }
    turnTo(Math.max(0, first), true);
  }

  function again() {
    end.hidden = true;
    turnTo(0, true);
  }

  function lineStep(d) {
    wanted = true;
    var p = page();
    if (!ready(p)) return;
    var k = li + d;
    if (k < 0) { if (pi > 0) turnTo(pi - 1, true); return; }
    if (k >= p.lines.length) { stop(); endOfPage(); return; }
    stop();
    playLine(k);
  }

  // ---------- zoom (small screens): panel to panel ----------

  // The camera frames the panel a line is in (read.json's frame: the
  // panel, or the whole page for a line in none). It stays put while the
  // lines are in the same panel and glides to the next panel when the
  // story moves on.
  var PAGE = { x1: 0, y1: 0, x2: 1, y2: 1 };
  function frameOf(l) { return (l && l.frame) || PAGE; }

  function zoomTo(b, again) {
    if (!zoom) return;
    var key = [b.x1, b.y1, b.x2, b.y2].join();
    if (key === camera && !again) return;
    camera = key;
    var W = sheet.clientWidth, H = sheet.clientHeight, SW = stage.clientWidth, SH = stage.clientHeight;
    var fw = Math.max(0.05, b.x2 - b.x1) * W, fh = Math.max(0.05, b.y2 - b.y1) * H;
    // As big as the panel fits the stage, with a little room round it.
    var s = Math.min(3, 0.94 * Math.min(SW / fw, SH / fh));
    if (s < 1.04) { sheet.style.transform = ''; sheet.classList.remove('is-zoomed'); return; }
    var cx = (b.x1 + b.x2) / 2 * W * s, cy = (b.y1 + b.y2) / 2 * H * s;
    var ox = (SW - W) / 2, oy = (SH - H) / 2; // where the sheet sits unzoomed
    var x = SW / 2 - cx - ox, y = SH / 2 - cy - oy;
    // Never past the page's edge where the page can fill the stage.
    x = Math.min(-ox, Math.max(SW - W * s - ox, x));
    y = Math.min(-oy, Math.max(SH - H * s - oy, y));
    if (W * s < SW) x = (SW - W * s) / 2 - ox;
    if (H * s < SH) y = (SH - H * s) / 2 - oy;
    sheet.style.transform = 'translate(' + x + 'px,' + y + 'px) scale(' + s + ')';
    sheet.classList.add('is-zoomed');
  }
  function unzoom() {
    camera = '';
    sheet.style.transform = '';
    sheet.classList.remove('is-zoomed');
  }

  // ---------- screen and media keys ----------

  function keepAwake() {
    if (lock || !('wakeLock' in navigator)) return;
    navigator.wakeLock.request('screen').then(function (l) { lock = l; l.addEventListener('release', function () { lock = null; }); }).catch(function () {});
  }
  function release() {
    if (lock && !wanted) { lock.release().catch(function () {}); lock = null; }
  }
  if ('mediaSession' in navigator) {
    try {
      navigator.mediaSession.setActionHandler('play', toggle);
      navigator.mediaSession.setActionHandler('pause', toggle);
      navigator.mediaSession.setActionHandler('nexttrack', function () { lineStep(1); });
      navigator.mediaSession.setActionHandler('previoustrack', function () { lineStep(-1); });
    } catch (e) {}
  }

  // ---------- controls ----------

  root.addEventListener('click', function (e) {
    var b = e.target.closest('[data-act]');
    if (!b) return;
    switch (b.dataset.act) {
      case 'start': begin(); break;
      case 'play': toggle(); break;
      case 'again': again(); break;
      case 'retry-page': wanted = true; retryPage(); break;
      case 'prev': lineStep(-1); break;
      case 'next': lineStep(1); break;
      case 'prev-page': if (!cover.hidden) break; end.hidden = true; turnTo(Math.max(0, pi - 1), wanted || playing); break;
      case 'next-page': if (!cover.hidden) break; if (pi + 1 < data.pages.length) turnTo(pi + 1, wanted || playing); break;
      case 'speed':
        rate = SPEEDS[(SPEEDS.indexOf(rate) + 1) % SPEEDS.length];
        audio.playbackRate = rate;
        b.textContent = rate + '×';
        break;
      case 'turn':
        autoTurn = !autoTurn;
        b.setAttribute('aria-pressed', String(autoTurn));
        break;
      case 'zoom':
        zoom = !zoom;
        b.setAttribute('aria-pressed', String(zoom));
        if (zoom && li >= 0 && page().lines[li]) zoomTo(frameOf(page().lines[li]), true); else unzoom();
        break;
    }
  });
  zoomBtn.setAttribute('aria-pressed', String(zoom));

  document.addEventListener('keydown', function (e) {
    if (e.target.closest('input, textarea, select') || e.metaKey || e.ctrlKey || e.altKey) return;
    var keys = { ' ': 'play', ArrowLeft: 'prev', ArrowRight: 'next', ArrowUp: 'prev-page', ArrowDown: 'next-page', PageUp: 'prev-page', PageDown: 'next-page' };
    var act = keys[e.key];
    if (!act) return;
    e.preventDefault();
    var b = root.querySelector('[data-act="' + act + '"]');
    if (b) b.click();
  });

  window.addEventListener('resize', function () {
    if (zoom && li >= 0 && page() && page().lines[li]) zoomTo(frameOf(page().lines[li]), true);
  });

  load().then(function () { prepare(false); tick(); }).catch(function () {
    status.textContent = 'The book would not open. Reload the page to try again.';
  });
})();
