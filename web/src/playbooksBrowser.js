import {draftFields, hasUnsavedPlaybookEdits} from './playbookDrafts.js';

export function focusInView(element) {
  element?.focus({preventScroll: true});
  element?.scrollIntoView({block: 'nearest', behavior: 'instant'});
}

export function focusFirstDraftError(form, errors) {
  const field = Object.keys(draftFields).find(key => errors[key]);
  if (field) focusInView(form?.querySelector(`[name="${field}"]`));
}

export function warnBeforeDraftUnload(target, dirty) {
  if (!dirty) return () => {};
  const warn = event => {
    event.preventDefault();
    event.returnValue = '';
  };
  target.addEventListener('beforeunload', warn);
  return () => target.removeEventListener('beforeunload', warn);
}

export function confirmDraftNavigation(session, confirm = globalThis.confirm) {
  return !hasUnsavedPlaybookEdits(session) || confirm('This draft has unsaved edits. Navigation will not save them. They stay in this tab until you save or cancel. Continue?');
}
