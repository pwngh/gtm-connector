import {CornerDownLeft, type LucideIcon, Search} from 'lucide-react';
import {type KeyboardEvent, useEffect, useId, useRef, useState} from 'react';
import './palette.css';

export interface PaletteItem {
  group: string;
  label: string;
  sub: string;
  icon?: LucideIcon;
  onSelect: () => void;
}

export interface PaletteProps {
  items: PaletteItem[];
  inputLabel: string;
  placeholder: string;
  empty: string;
}

/** A filter box over a list of places: arrows move, Enter opens. Its dialog holds it. */
export function Palette({items, inputLabel, placeholder, empty}: PaletteProps) {
  const [query, setQuery] = useState('');
  const [active, setActive] = useState(0);
  const listId = useId();
  const options = useRef<(HTMLButtonElement | null)[]>([]);
  const normalized = query.trim().toLowerCase();
  const results = items.filter(item => `${item.label} ${item.sub} ${item.group}`.toLowerCase().includes(normalized));
  const index = Math.min(active, Math.max(0, results.length - 1));
  // biome-ignore lint/correctness/useExhaustiveDependencies: the chosen option comes into view when the list or the choice changes.
  useEffect(() => {
    options.current[index]?.scrollIntoView({block: 'nearest'});
  }, [index, query]);
  function handleKey(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
      event.preventDefault();
      if (results.length) {
        setActive((index + (event.key === 'ArrowDown' ? 1 : -1) + results.length) % results.length);
      }
    } else if (event.key === 'Enter') {
      const chosen = results[index];
      if (chosen) {
        event.preventDefault();
        chosen.onSelect();
      }
    }
  }
  return (
    <div className="ui-palette">
      <div className="ui-palette__input">
        <Search
          size={20}
          aria-hidden="true"
        />
        <input
          className="ui-palette__field"
          role="combobox"
          aria-label={inputLabel}
          aria-controls={listId}
          aria-expanded="true"
          aria-autocomplete="list"
          aria-activedescendant={results.length ? `${listId}-${index}` : undefined}
          placeholder={placeholder}
          value={query}
          onChange={event => {
            setQuery(event.target.value);
            setActive(0);
          }}
          onKeyDown={handleKey}
        />
      </div>
      <div
        className="ui-palette__results"
        id={listId}
        role="listbox"
        aria-label={inputLabel}
      >
        {results.map((item, i) => {
          const Icon = item.icon;
          const groupStarts = i === 0 || results[i - 1]?.group !== item.group;
          return (
            <div
              key={`${item.group}:${item.label}`}
              role="presentation"
            >
              {groupStarts && (
                <div
                  className="ui-palette__group"
                  role="presentation"
                >
                  {item.group}
                </div>
              )}
              <button
                ref={node => {
                  options.current[i] = node;
                }}
                type="button"
                tabIndex={-1}
                role="option"
                id={`${listId}-${i}`}
                aria-selected={i === index}
                className="ui-palette__option"
                onMouseMove={() => setActive(i)}
                onClick={item.onSelect}
              >
                {Icon && (
                  <Icon
                    size={20}
                    aria-hidden="true"
                  />
                )}
                <span className="ui-palette__copy">
                  <strong className="ui-palette__label">{item.label}</strong>
                  <span className="ui-palette__sub">{item.sub}</span>
                </span>
                <CornerDownLeft
                  className="ui-palette__hint"
                  size={16}
                  aria-hidden="true"
                />
              </button>
            </div>
          );
        })}
        {!results.length && <p className="ui-palette__empty">{empty}</p>}
      </div>
    </div>
  );
}
