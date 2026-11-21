import {type ReactNode, useEffect} from 'react';

/** Which deal the address names, as far as the server has vouched for it. */
export type Named = {kind: 'reading'} | {kind: 'invalid'} | {kind: 'unverified'; account: string} | {kind: 'verified'; account: string; name: string};

const headings = {reading: 'Reading the deal', invalid: 'No deal open', unverified: 'Account not verified'};

function idLine(named: Named): string | null {
  switch (named.kind) {
    case 'verified':
      return named.account;
    case 'unverified':
      return `Account ID ${named.account}, not verified`;
    case 'invalid':
      return 'The account in this link is not an account ID. It was not read.';
    default:
      return null;
  }
}

// The deal's name at the title step and its ID under it. A name is shown only
// when the account list the server sent holds it; an ID from the address is
// shown as not verified, and nothing is named while the list is read. The
// browser tab's title is the name. Lines about the open card follow in the
// meta step.
export function Title({named, children}: {named: Named; children?: ReactNode}) {
  const name = named.kind === 'verified' ? named.name : null;
  useEffect(() => {
    if (name === null) {
      return undefined;
    }
    const before = document.title;
    document.title = name;
    return () => {
      document.title = before;
    };
  }, [name]);
  const id = idLine(named);
  return (
    <header className="dc-head">
      <h1
        id="dc-title"
        className="dc-title"
      >
        {named.kind === 'verified' ? named.name : headings[named.kind]}
      </h1>
      {id !== null && <p className="dc-id">{id}</p>}
      {children}
    </header>
  );
}
