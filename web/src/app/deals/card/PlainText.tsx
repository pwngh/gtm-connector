import {useState} from 'react';
import {Button} from '../../../ui/Button.tsx';

/** What the status line says after a copy. */
const copied = 'Copied';
const copyRefused = 'This browser refused to copy. Select the text and copy it with the keyboard.';

// The plain-text card whole, as the command line prints it: one text node,
// white-space kept, long lines wrapped. The button copies the same string.
export function PlainText({text}: {text: string}) {
  const [said, setSaid] = useState('');
  const copy = async () => {
    setSaid('');
    try {
      if (!navigator.clipboard) {
        throw new Error('no clipboard');
      }
      await navigator.clipboard.writeText(text);
      setSaid(copied);
    } catch {
      setSaid(copyRefused);
    }
  };
  return (
    <section
      className="dc-plain"
      aria-label="The plain-text card"
    >
      <div className="dc-plain__bar">
        <Button onClick={copy}>Copy the plain-text card</Button>
        <p
          className="dc-plain__status"
          role="status"
        >
          {said}
        </p>
      </div>
      <pre className="dc-plain__text">{text}</pre>
    </section>
  );
}
