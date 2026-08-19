import { forwardRef, type HTMLAttributes, type ReactNode } from 'react';
import clsx from 'clsx';
import './Sidebar.css';

export interface SidebarItem {
  key: string;
  label: ReactNode;
  icon?: ReactNode;
  /** Small trailing indicator — a count, a status dot. */
  badge?: ReactNode;
  disabled?: boolean;
  /** Renders as a link instead of a button when set. */
  href?: string;
}

export interface SidebarSection {
  key: string;
  /** Omit for an unlabelled group. */
  title?: string;
  items: SidebarItem[];
}

export interface SidebarProps extends Omit<HTMLAttributes<HTMLElement>, 'onSelect'> {
  sections: SidebarSection[];
  activeKey?: string;
  onSelect?: (key: string) => void;
  /** Brand / product mark, pinned to the top. */
  header?: ReactNode;
  /** Pinned to the bottom — account, version, theme switch. */
  footer?: ReactNode;
  /** Icon-only mode. Labels move to `title` and to the accessible name. */
  collapsed?: boolean;
  /** Accessible name for the nav landmark. Default `'Main'`. */
  label?: string;
  /**
   * Renders an item as a custom element — a router Link, for instance.
   * Receives everything needed to render, so the framework never has to know
   * about any router.
   */
  renderItem?: (item: SidebarItem, props: SidebarItemRenderProps) => ReactNode;
}

export interface SidebarItemRenderProps {
  active: boolean;
  collapsed: boolean;
  className: string;
  'aria-current': 'page' | undefined;
  onClick: () => void;
  children: ReactNode;
}

/**
 * Primary navigation.
 *
 * Uses `aria-current="page"` rather than `aria-selected` — the items navigate
 * rather than select within a widget, and `aria-selected` outside a
 * listbox/tab/grid role is meaningless to assistive tech.
 *
 * Collapsed mode keeps the accessible name intact: the label is still rendered,
 * just visually hidden, so an icon-only rail is fully usable with a screen
 * reader. Sighted users get the label back via `title`.
 */
export const Sidebar = forwardRef<HTMLElement, SidebarProps>(function Sidebar(
  {
    sections,
    activeKey,
    onSelect,
    header,
    footer,
    collapsed = false,
    label = 'Main',
    renderItem,
    className,
    ...rest
  },
  ref,
) {
  return (
    <nav
      {...rest}
      ref={ref}
      data-stratum="sidebar"
      data-collapsed={collapsed || undefined}
      className={clsx('stratum-sidebar', className)}
      aria-label={label}
    >
      {header && <div className="stratum-sidebar__header">{header}</div>}

      <div className="stratum-sidebar__scroll">
        {sections.map((section) => (
          <div key={section.key} className="stratum-sidebar__section">
            {section.title && !collapsed && (
              <h2 className="stratum-sidebar__section-title">{section.title}</h2>
            )}
            {section.title && collapsed && (
              <span className="stratum-sidebar__section-rule" role="separator" aria-label={section.title} />
            )}

            <ul className="stratum-sidebar__list">
              {section.items.map((item) => {
                const active = item.key === activeKey;
                const inner = (
                  <>
                    {item.icon && (
                      <span className="stratum-sidebar__icon" aria-hidden="true">
                        {item.icon}
                      </span>
                    )}
                    <span
                      className={clsx(
                        'stratum-sidebar__label',
                        collapsed && 'stratum-visually-hidden',
                      )}
                    >
                      {item.label}
                    </span>
                    {item.badge && !collapsed && (
                      <span className="stratum-sidebar__badge">{item.badge}</span>
                    )}
                  </>
                );

                const renderProps: SidebarItemRenderProps = {
                  active,
                  collapsed,
                  className: 'stratum-sidebar__item',
                  'aria-current': active ? 'page' : undefined,
                  onClick: () => onSelect?.(item.key),
                  children: inner,
                };

                return (
                  <li key={item.key} data-active={active || undefined}>
                    {renderItem ? (
                      renderItem(item, renderProps)
                    ) : item.href ? (
                      <a
                        href={item.href}
                        className="stratum-sidebar__item"
                        aria-current={active ? 'page' : undefined}
                        aria-disabled={item.disabled || undefined}
                        title={collapsed && typeof item.label === 'string' ? item.label : undefined}
                        onClick={() => onSelect?.(item.key)}
                      >
                        {inner}
                      </a>
                    ) : (
                      <button
                        type="button"
                        className="stratum-sidebar__item"
                        aria-current={active ? 'page' : undefined}
                        disabled={item.disabled}
                        title={collapsed && typeof item.label === 'string' ? item.label : undefined}
                        onClick={() => onSelect?.(item.key)}
                      >
                        {inner}
                      </button>
                    )}
                  </li>
                );
              })}
            </ul>
          </div>
        ))}
      </div>

      {footer && <div className="stratum-sidebar__footer">{footer}</div>}
    </nav>
  );
});
