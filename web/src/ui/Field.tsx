import {type ComponentProps, useId} from 'react';
import './field.css';

export interface FieldProps extends Omit<ComponentProps<'input'>, 'id' | 'className'> {
  label: string;
}

/** A labelled text input. */
export function Field({label, ...input}: FieldProps) {
  const id = useId();
  return (
    <div className="ui-field">
      <label
        className="ui-field__label"
        htmlFor={id}
      >
        {label}
      </label>
      <input
        {...input}
        id={id}
        className="ui-field__input"
      />
    </div>
  );
}
