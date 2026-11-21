import {Link, useLocation} from 'react-router';
import {unitParam, unitSearch} from './proof.ts';

// A unit's number in a bordered box, written as on the text card: [5].
// Pressing it puts ?unit=5 in the address, which opens the proof drawer on
// that unit. The number whose unit is open is marked.
export function Cite({n}: {n: number}) {
  const {search} = useLocation();
  const open = new URLSearchParams(search).get(unitParam) === String(n);
  return (
    <Link
      className="dc-cite"
      to={{search: unitSearch(search, n)}}
      preventScrollReset
      aria-label={`Source ${n}`}
      data-cite={n}
      {...(open ? {'data-open': ''} : {})}
    >
      <span className="dc-cite__box">{`[${n}]`}</span>
    </Link>
  );
}
