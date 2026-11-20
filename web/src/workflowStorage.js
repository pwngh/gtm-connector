import {compactHistoryState} from './historyRecording.js';

export const workflowStorageKey = 'gtm-workflow-demo-v1';

export function saveWorkflowState(state, storage) {
  try {
    const target = storage ?? globalThis.localStorage;
    target.setItem(workflowStorageKey, JSON.stringify(compactHistoryState(state)));
    return {state, status: 'saved'};
  } catch {
    return {state, status: 'failed'};
  }
}
