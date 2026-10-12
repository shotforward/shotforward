import { useEffect, useRef, type ReactNode } from 'react';

interface DialogProps {
  open: boolean;
  label: string;
  children: ReactNode;
  onClose: () => void;
}

export function Dialog({ open, label, children, onClose }: DialogProps) {
  const reference = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const dialog = reference.current;
    if (!dialog) return;
    if (open && !dialog.open) dialog.showModal();
    if (!open && dialog.open) dialog.close();
  }, [open]);

  return (
    <dialog
      ref={reference}
      className="dialog"
      aria-label={label}
      onClick={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
      onClose={onClose}
    >
      {children}
    </dialog>
  );
}
