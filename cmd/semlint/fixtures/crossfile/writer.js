// Draft persistence, write side.
//
// Each side of this pair is correct on its own. The defect only exists in the
// relationship between them, which is what makes it a cross-file case.

(function () {
  'use strict';

  const DRAFT_PREFIX = 'gofastr.draft.';

  // Save the in-progress value for a field.
  function saveDraft(fieldName, value) {
    try {
      localStorage.setItem(DRAFT_PREFIX + encodeURIComponent(fieldName), value);
    } catch (_) {}
  }

  window.__draftWriter = { saveDraft: saveDraft };
})();
