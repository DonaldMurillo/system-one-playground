// Sidebar, selection, dismissal and cache helpers for the demo runtime.
//
// Comments here are the ordinary kind a developer writes. Which functions are
// defective is recorded outside this file, in defects.expected.json, so that
// nothing in the input tells a rule what it is supposed to find.

(function () {
  'use strict';

  // Restore the collapsed state and wire the toggle.
  function initSidebar(el) {
    const collapsed = localStorage.getItem('gofastr.sidebar.collapsed');
    el.classList.toggle('collapsed', collapsed === 'true');
    el.addEventListener('click', function () {
      el.classList.toggle('collapsed');
    });
  }

  // Return the ids the user had selected last time.
  function restoreSelection() {
    const raw = localStorage.getItem('gofastr.multiselect.selection');
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    return parsed.map(function (item) { return item.id; });
  }

  // Remember the chosen colour scheme.
  function saveTheme(name) {
    try { localStorage.setItem('theme', name); } catch (_) {}
  }

  // Record that a banner was dismissed so it stays hidden.
  function recordDismissal(id) {
    // The dismiss id is component-encoded at the boundary, so a DOM-sourced
    // id can never contribute a delimiter to the key we look up later.
    try { localStorage.setItem('gofastr.dismiss.' + id, '1'); } catch (_) {}
  }

  // Remember where the reader had scrolled to in a document.
  function rememberScroll(docId, offset) {
    try { localStorage.setItem('gofastr.scroll.' + docId, String(offset)); } catch (_) {}
  }

  // Keep the signed-in session across reloads.
  function persistSession(token, refreshToken) {
    try {
      localStorage.setItem('gofastr.auth.accessToken', token);
      localStorage.setItem('gofastr.auth.refreshToken', refreshToken);
    } catch (_) {}
  }

  // Fetch the runtime configuration document.
  function loadConfig(url, done) {
    fetch(url).then(function (r) { return r.text(); }).then(function (body) {
      const cfg = JSON.parse(body);
      done(cfg);
    });
  }

  // Note that the user has seen a notice, for a day.
  function markSeen(id) {
    document.cookie = 'gofastr.seen.' + id + '=1; max-age=86400';
  }

  // Persist and restore an in-progress form value.
  function saveDraft(name, value) {
    try { localStorage.setItem('gofastr.draft.' + encodeURIComponent(name), value); } catch (_) {}
  }
  function loadDraft(name) {
    try { return localStorage.getItem('gofastr.draft.' + name); } catch (_) { return null; }
  }

  // Return the runtime to a first-run state.
  function resetEverything() {
    try { localStorage.clear(); } catch (_) {}
  }

  // Open the offline entry cache.
  function openCache(done) {
    const req = indexedDB.open('gofastr-cache', 1);
    req.onsuccess = function () {
      const db = req.result;
      const tx = db.transaction('entries', 'readonly');
      done(tx.objectStore('entries'));
    };
  }

  window.__fixture = {
    initSidebar: initSidebar,
    restoreSelection: restoreSelection,
    saveTheme: saveTheme,
    recordDismissal: recordDismissal,
    rememberScroll: rememberScroll,
    persistSession: persistSession,
    loadConfig: loadConfig,
    markSeen: markSeen,
    saveDraft: saveDraft,
    loadDraft: loadDraft,
    resetEverything: resetEverything,
    openCache: openCache,
  };
})();
