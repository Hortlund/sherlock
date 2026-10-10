import { useEffect, useId, useRef } from "react";
import type { ReactNode } from "react";
import { X } from "lucide-react";

export function PageHeading({
  title,
  description,
  children,
}: {
  title: string;
  description: string;
  children?: ReactNode;
}) {
  return (
    <header className="page-heading">
      <div>
        <h1>{title}</h1>
        <p className="page-description">{description}</p>
      </div>
      {children}
    </header>
  );
}
export function StatusBadge({
  value,
  children,
}: {
  value: string;
  children?: ReactNode;
}) {
  return (
    <span className={`status-badge status-${value}`}>
      {children ?? value.replaceAll("_", " ")}
    </span>
  );
}
export function Drawer({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
}) {
  const dialog = useRef<HTMLDialogElement>(null),
    heading = useId();
  useEffect(() => {
    const node = dialog.current!;
    const previous = document.activeElement as HTMLElement | null;
    const overflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    node.showModal();
    return () => {
      node.close();
      document.body.style.overflow = overflow;
      previous?.focus();
    };
  }, []);
  return (
    <dialog
      ref={dialog}
      className="editor-drawer"
      aria-labelledby={heading}
      onKeyDown={(e) => {
        if (e.key !== "Tab") return;
        const controls = Array.from(
          e.currentTarget.querySelectorAll<HTMLElement>(
            "button, input, select, textarea, a[href], summary, [tabindex]",
          ),
        ).filter(
          (node) =>
            node.tabIndex >= 0 &&
            !node.matches(":disabled") &&
            node.getClientRects().length > 0,
        );
        const first = controls[0],
          last = controls[controls.length - 1];
        if (e.shiftKey && document.activeElement === first) {
          e.preventDefault();
          last?.focus();
        } else if (!e.shiftKey && document.activeElement === last) {
          e.preventDefault();
          first?.focus();
        }
      }}
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
    >
      <header className="drawer-heading">
        <h2 id={heading}>{title}</h2>
        <button
          type="button"
          className="icon-button"
          aria-label="Close editor"
          onClick={onClose}
        >
          <X size={20} />
        </button>
      </header>
      <div className="drawer-body">{children}</div>
    </dialog>
  );
}
