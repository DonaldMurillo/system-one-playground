// Draft persistence, read side.

(function () {
  'use strict';

  const DRAFT_PREFIX = 'gofastr.draft.';

  // Restore the saved value for a field.
  function loadDraft(fieldName) {
    try {
      return localStorage.getItem(DRAFT_PREFIX + fieldName);
    } catch (_) {
      return null;
    }
  }

  window.__draftReader = { loadDraft: loadDraft };
})();
