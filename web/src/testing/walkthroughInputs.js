import {walkthroughFields} from '../customerDecision.js';

// What an operator types in each dialog. Tests supply these explicitly; the app never fills them in.
export const typedInputs = {
  verify_document: {reason: 'Typed in a test: version 4 is the version Agreement A-17 names, and the file meets the specifications.'},
  inspect_approval_letter: {outcome: 'accepted', reason: 'Typed in a test: signed and legible, and it names the agreement and the version under review.'},
  reject_document: {reason: 'Typed in a test: this is not the version the agreement record names.'},
};

// One walkthrough command with its signed-in principal and the operator's typed input.
export const walkthroughAction = (state, type, extra = {}) => ({type, ...walkthroughFields(state, type), ...(typedInputs[type] || {}), ...extra});

// The same fields without the command type, for helpers that spread them into an action.
export const walkthroughInputs = (state, type) => ({...walkthroughFields(state, type), ...(typedInputs[type] || {})});
