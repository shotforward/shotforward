import { useEffect, useId, useRef, useState } from 'react';

export interface SelectOption<Value extends string> {
  value: Value;
  label: string;
  description?: string;
  icon?: string;
}

export function Select<Value extends string>({
  value,
  options,
  placeholder = '',
  placeholderIcon = '◇',
  ariaLabel,
  disabled = false,
  compact = false,
  placement = 'auto',
  className = '',
  onChange,
}: {
  value: Value | '';
  options: readonly SelectOption<Value>[];
  placeholder?: string;
  placeholderIcon?: string;
  ariaLabel: string;
  disabled?: boolean;
  compact?: boolean;
  placement?: 'auto' | 'top' | 'bottom';
  className?: string;
  onChange: (value: Value) => void;
}) {
  const id = useId();
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const typeahead = useRef('');
  const typeaheadTimer = useRef<number | undefined>(undefined);
  const selectedIndex = options.findIndex((option) => option.value === value);
  const selected = selectedIndex >= 0 ? options[selectedIndex] : undefined;
  const rich = options.some((option) => option.icon || option.description);
  const hasIcons = options.some((option) => option.icon);
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(Math.max(0, selectedIndex));
  const [resolvedPlacement, setResolvedPlacement] = useState<'top' | 'bottom'>(
    placement === 'top' ? 'top' : 'bottom',
  );
  const [alignEnd, setAlignEnd] = useState(false);

  useEffect(() => {
    if (!open) return;
    const closeFromOutside = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false);
    };
    const closeFromViewportChange = () => setOpen(false);
    document.addEventListener('pointerdown', closeFromOutside);
    window.addEventListener('resize', closeFromViewportChange);
    return () => {
      document.removeEventListener('pointerdown', closeFromOutside);
      window.removeEventListener('resize', closeFromViewportChange);
    };
  }, [open]);

  useEffect(
    () => () => {
      if (typeaheadTimer.current !== undefined) window.clearTimeout(typeaheadTimer.current);
    },
    [],
  );

  const show = () => {
    if (disabled || options.length === 0) return;
    if (placement === 'auto' && trigger.current) {
      const bounds = trigger.current.getBoundingClientRect();
      const estimatedHeight = Math.min(options.length * (rich ? 54 : 38) + 12, 280);
      const estimatedWidth = Math.min(Math.max(bounds.width, 180), 320);
      const below = window.innerHeight - bounds.bottom - 12;
      const above = bounds.top - 12;
      setResolvedPlacement(below < estimatedHeight && above > below ? 'top' : 'bottom');
      setAlignEnd(bounds.left + estimatedWidth > window.innerWidth - 12);
    } else {
      setResolvedPlacement(placement === 'top' ? 'top' : 'bottom');
      if (trigger.current) {
        const bounds = trigger.current.getBoundingClientRect();
        const estimatedWidth = Math.min(Math.max(bounds.width, 180), 320);
        setAlignEnd(bounds.left + estimatedWidth > window.innerWidth - 12);
      }
    }
    setActiveIndex(Math.max(0, selectedIndex));
    setOpen(true);
  };

  const choose = (index: number) => {
    const option = options[index];
    if (!option) return;
    onChange(option.value);
    setOpen(false);
    trigger.current?.focus();
  };

  const move = (offset: number) => {
    if (!options.length) return;
    if (!open) {
      show();
      return;
    }
    setActiveIndex((current) => (current + offset + options.length) % options.length);
  };

  const searchFromKeyboard = (key: string) => {
    typeahead.current += key.toLocaleLowerCase();
    if (typeaheadTimer.current !== undefined) window.clearTimeout(typeaheadTimer.current);
    typeaheadTimer.current = window.setTimeout(() => {
      typeahead.current = '';
    }, 500);
    const match = options.findIndex((option) =>
      option.label.toLocaleLowerCase().startsWith(typeahead.current),
    );
    if (match >= 0) {
      if (!open) show();
      setActiveIndex(match);
    }
  };

  const classes = [
    'select-control',
    rich && 'select-control--rich',
    hasIcons && 'select-control--with-icons',
    compact && 'select-control--compact',
    resolvedPlacement === 'top' && 'select-control--top',
    alignEnd && 'select-control--align-end',
    className,
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <div className={classes} ref={root}>
      <button
        ref={trigger}
        className="select-control__trigger"
        type="button"
        role="combobox"
        aria-label={ariaLabel}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={`${id}-listbox`}
        aria-activedescendant={open ? `${id}-option-${activeIndex}` : undefined}
        disabled={disabled || options.length === 0}
        onClick={() => (open ? setOpen(false) : show())}
        onKeyDown={(event) => {
          if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
            event.preventDefault();
            move(event.key === 'ArrowDown' ? 1 : -1);
          } else if (event.key === 'Enter' || event.key === ' ') {
            event.preventDefault();
            if (open) choose(activeIndex);
            else show();
          } else if (event.key === 'Escape') {
            event.preventDefault();
            setOpen(false);
          } else if (event.key === 'Home' && open) {
            event.preventDefault();
            setActiveIndex(0);
          } else if (event.key === 'End' && open) {
            event.preventDefault();
            setActiveIndex(options.length - 1);
          } else if (event.key === 'Tab') {
            setOpen(false);
          } else if (event.key.length === 1 && !event.ctrlKey && !event.metaKey && !event.altKey) {
            searchFromKeyboard(event.key);
          }
        }}
      >
        {hasIcons && (
          <span className="select-control__icon" aria-hidden="true">
            {selected?.icon ?? placeholderIcon}
          </span>
        )}
        <span className="select-control__value">
          <strong>{selected?.label ?? placeholder}</strong>
          {selected?.description && <small>{selected.description}</small>}
        </span>
        <svg className="select-control__chevron" viewBox="0 0 20 20" aria-hidden="true">
          <path d="m5.5 7.5 4.5 4.5 4.5-4.5" />
        </svg>
      </button>
      {open && (
        <div className="select-control__popover" id={`${id}-listbox`} role="listbox" aria-label={ariaLabel}>
          {options.map((option, index) => (
            <button
              id={`${id}-option-${index}`}
              key={option.value}
              className={index === activeIndex ? 'is-active' : ''}
              type="button"
              role="option"
              aria-label={option.label}
              aria-describedby={option.description ? `${id}-option-${index}-description` : undefined}
              aria-selected={option.value === value}
              tabIndex={-1}
              onMouseEnter={() => setActiveIndex(index)}
              onClick={() => choose(index)}
            >
              {hasIcons && (
                <span className="select-control__icon" aria-hidden="true">
                  {option.icon ?? '·'}
                </span>
              )}
              <span className="select-control__option-copy">
                <strong>{option.label}</strong>
                {option.description && (
                  <small id={`${id}-option-${index}-description`}>{option.description}</small>
                )}
              </span>
              <svg className="select-control__check" viewBox="0 0 20 20" aria-hidden="true">
                {option.value === value && <path d="m5 10.3 3.1 3.1L15.2 6.7" />}
              </svg>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
