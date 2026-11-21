import './placeholder.css';

export interface PlaceholderProps {
  /** The one line that says what is being read. */
  status: string;
}

/** A static shape of what is coming, and one status line. Nothing moves. */
export function Placeholder({status}: PlaceholderProps) {
  return (
    <div className="ui-placeholder">
      <div
        className="ui-placeholder__shape"
        aria-hidden="true"
      >
        <span className="ui-placeholder__line" />
        <span className="ui-placeholder__line" />
        <span className="ui-placeholder__line" />
      </div>
      <p
        className="ui-placeholder__status"
        role="status"
        aria-live="polite"
      >
        {status}
      </p>
    </div>
  );
}
