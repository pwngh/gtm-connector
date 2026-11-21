import {Link} from 'react-router';
import {unitHref} from './card.ts';

// A unit's number in a bordered box, written as on the text card: [5]. It
// links to the page that shows that unit of this card in full.
export function Cite({account, card, n}: {account: string; card: string; n: number}) {
  return (
    <Link
      className="dc-cite"
      to={unitHref(account, card, n)}
      aria-label={`Source ${n}`}
    >
      <span className="dc-cite__box">{`[${n}]`}</span>
    </Link>
  );
}
