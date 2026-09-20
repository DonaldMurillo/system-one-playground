// Fixture: the same features as defects.js, written correctly. A rule that
// fires here is a false positive. This half matters more than the other: a
// linter that flags good code gets switched off, and then it catches nothing.

(function () {
  'use strict';

  const PREFIX = 'gofastr.demo.';
  const key = function (name) { return PREFIX + encodeURIComponent(name); };

  // Guarded: a throw leaves the default in place and the handler still binds.
  // Another tab changing the value is picked up through the storage event, so
  // two open tabs do not drift apart.
  function initSidebar(el) {
    const readCollapsed = function () {
      try { return localStorage.getItem(key('sidebar.collapsed')) === 'true'; }
      catch (_) { return false; }
    };
    el.classList.toggle('collapsed', readCollapsed());
    el.addEventListener('click', function () {
      el.classList.toggle('collapsed');
    });
    window.addEventListener('storage', function (ev) {
      if (ev.key === key('sidebar.collapsed')) {
        el.classList.toggle('collapsed', readCollapsed());
      }
    });
  }

  // The parsed value is checked before anything structural is done with it.
  function restoreSelection() {
    let raw = null;
    try { raw = localStorage.getItem(key('multiselect.selection')); } catch (_) { return []; }
    if (!raw) return [];
    let parsed;
    try { parsed = JSON.parse(raw); } catch (_) { return []; }
    if (!Array.isArray(parsed)) return [];
    return parsed.filter(function (item) {
      return item && typeof item.id === 'string';
    }).map(function (item) { return item.id; });
  }

  // Namespaced key, guarded write.
  function saveTheme(name) {
    try { localStorage.setItem(key('theme'), name); } catch (_) {}
  }

  // The comment describes exactly what the line below does. Dismissals are
  // kept in one fixed key rather than one key per id, so the set cannot grow
  // without bound.
  function recordDismissal(id) {
    try {
      // The id is percent-encoded before it joins the set, so a DOM-sourced id
      // cannot contribute a delimiter.
      const raw = localStorage.getItem(key('dismissed'));
      let ids = [];
      try { ids = raw ? JSON.parse(raw) : []; } catch (_) { ids = []; }
      if (!Array.isArray(ids)) ids = [];
      const encoded = encodeURIComponent(id);
      if (ids.indexOf(encoded) === -1) ids.push(encoded);
      while (ids.length > 200) ids.shift();
      localStorage.setItem(key('dismissed'), JSON.stringify(ids));
    } catch (_) {}
  }

  // Bounded: the most recent entries are kept and the rest are dropped.
  const SCROLL_LIMIT = 50;
  function rememberScroll(docId, offset) {
    try {
      const indexRaw = localStorage.getItem(key('scroll.index'));
      let index = [];
      try { index = indexRaw ? JSON.parse(indexRaw) : []; } catch (_) { index = []; }
      if (!Array.isArray(index)) index = [];
      index = index.filter(function (d) { return d !== docId; });
      index.push(docId);
      while (index.length > SCROLL_LIMIT) {
        const evicted = index.shift();
        localStorage.removeItem(key('scroll.' + evicted));
      }
      localStorage.setItem(key('scroll.' + docId), String(offset));
      localStorage.setItem(key('scroll.index'), JSON.stringify(index));
    } catch (_) {}
  }

  // Interface state only; nothing here identifies or authenticates anyone.
  // All pane widths live under one key, so the set cannot grow without bound.
  function persistPanePreference(paneId, width) {
    try {
      const raw = localStorage.getItem(key('pane.widths'));
      let widths = {};
      try { widths = raw ? JSON.parse(raw) : {}; } catch (_) { widths = {}; }
      if (!widths || typeof widths !== 'object') widths = {};
      widths[paneId] = width;
      localStorage.setItem(key('pane.widths'), JSON.stringify(widths));
    } catch (_) {}
  }

  // Guarded parse of a network response.
  function loadConfig(url, done, fail) {
    fetch(url).then(function (r) { return r.text(); }).then(function (body) {
      let cfg;
      try { cfg = JSON.parse(body); } catch (_) { fail(new Error('malformed config')); return; }
      done(cfg);
    }).catch(fail);
  }

  // Both attributes present.
  function markSeen(id) {
    document.cookie = key('seen.' + id) + '=1; path=/; max-age=86400; SameSite=Lax';
  }

  // Both sides go through the same key builder, and drafts are held in one
  // object under a single key so they cannot accumulate without bound.
  function readDrafts() {
    try {
      const raw = localStorage.getItem(key('drafts'));
      const v = raw ? JSON.parse(raw) : {};
      return v && typeof v === 'object' ? v : {};
    } catch (_) { return {}; }
  }
  function saveDraft(name, value) {
    try {
      const drafts = readDrafts();
      drafts[name] = value;
      localStorage.setItem(key('drafts'), JSON.stringify(drafts));
    } catch (_) {}
  }
  function loadDraft(name) {
    const v = readDrafts()[name];
    return typeof v === 'string' ? v : null;
  }

  // Removes only this library's keys, leaving the host application's alone.
  function resetOwnKeys() {
    try {
      const doomed = [];
      for (let i = 0; i < localStorage.length; i++) {
        const k = localStorage.key(i);
        if (k && k.indexOf(PREFIX) === 0) doomed.push(k);
      }
      doomed.forEach(function (k) { localStorage.removeItem(k); });
    } catch (_) {}
  }

  // Upgrade and failure paths are both handled.
  function openCache(done, fail) {
    const req = indexedDB.open('gofastr-demo-cache', 1);
    req.onupgradeneeded = function () {
      const db = req.result;
      if (!db.objectStoreNames.contains('entries')) {
        db.createObjectStore('entries', { keyPath: 'id' });
      }
    };
    req.onerror = function () { fail(req.error); };
    req.onblocked = function () { fail(new Error('blocked by another tab')); };
    req.onsuccess = function () {
      const tx = req.result.transaction('entries', 'readonly');
      done(tx.objectStore('entries'));
    };
  }

  window.__fixtureClean = {
    initSidebar: initSidebar,
    restoreSelection: restoreSelection,
    saveTheme: saveTheme,
    recordDismissal: recordDismissal,
    rememberScroll: rememberScroll,
    persistPanePreference: persistPanePreference,
    loadConfig: loadConfig,
    markSeen: markSeen,
    saveDraft: saveDraft,
    loadDraft: loadDraft,
    resetOwnKeys: resetOwnKeys,
    openCache: openCache,
  };
})();
