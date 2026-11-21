import type {ComponentProps} from 'react';
import './button.css';

export interface ButtonProps extends Omit<ComponentProps<'button'>, 'type'> {
  type?: 'button' | 'submit';
  /** primary for the one action of a screen, quiet for a control in a bar. */
  variant?: 'primary' | 'plain' | 'quiet';
  /** A press is under way: the button stays focusable and says so. */
  pending?: boolean;
}

export function Button({type = 'button', variant = 'plain', pending = false, className, children, ...rest}: ButtonProps) {
  const classes = `ui-button ui-button--${variant}${className ? ` ${className}` : ''}`;
  return (
    <button
      {...rest}
      type={type}
      className={classes}
      aria-disabled={pending || undefined}
    >
      {children}
    </button>
  );
}
