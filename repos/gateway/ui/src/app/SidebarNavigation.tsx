import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { NavLink, useLocation } from "react-router-dom";
import { ChevronDown, ChevronRight } from "@gravity-ui/icons";
import { Icon, Popup } from "@gravity-ui/uikit";
import { activeNavigationGroup, isCurrentNavigationRoute, navigationIcon, type NavigationLink, type VisibleNavigationGroup, type VisibleNavigationSection } from "./navigation";
import { readCollapsedGroups, saveCollapsedGroups } from "./sidebarState";

function SidebarLink({ link, compact = false, onNavigate }: { link: NavigationLink; compact?: boolean; onNavigate?: () => void }) {
  return <NavLink to={link.route.path} className={({ isActive }) => `gateway-sidebar-link${isActive ? " active" : ""}`} aria-label={link.title} title={compact ? link.title : undefined} onClick={onNavigate}>
    <Icon data={navigationIcon(link.route.path)} size={20} />
    <span className={compact ? "gateway-sidebar-sr-only" : "gateway-sidebar-label"}>{link.title}</span>
    {!link.route.available && <span className="nav-dot" title="Backend unavailable" />}
  </NavLink>;
}

function SidebarGroup({ group, collapsed, compact, onToggle }: { group: VisibleNavigationGroup; collapsed: boolean; compact: boolean; onToggle: () => void }) {
  const { pathname } = useLocation();
  const [popupOpen, setPopupOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const children = useRef<HTMLUListElement>(null);
  const focusChildren = useRef(false);
  const current = group.links.some((link) => isCurrentNavigationRoute(link.route.path, pathname));
  const expanded = compact ? popupOpen : !collapsed;
  const childID = `sidebar-group-${group.id}${compact ? "-popup" : ""}`;
  useEffect(() => { setPopupOpen(false); }, [pathname, compact]);
  useEffect(() => {
    if (!compact && !collapsed && focusChildren.current) {
      focusChildren.current = false;
      children.current?.querySelector<HTMLAnchorElement>("a")?.focus();
    }
  }, [collapsed, compact]);
  const focusLinks = (event: KeyboardEvent<HTMLUListElement>) => {
    const links = Array.from(event.currentTarget.querySelectorAll<HTMLAnchorElement>("a[href]"));
    const index = links.indexOf(document.activeElement as HTMLAnchorElement);
    let next: number;
    if (event.key === "ArrowDown") next = (index + 1) % links.length;
    else if (event.key === "ArrowUp") next = (index - 1 + links.length) % links.length;
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = links.length - 1;
    else if (event.key === "ArrowLeft" && !compact) { event.preventDefault(); onToggle(); trigger.current?.focus(); return; }
    else return;
    event.preventDefault(); links[next]?.focus();
  };
  const triggerKey = (event: KeyboardEvent<HTMLButtonElement>) => {
    if (event.key === "ArrowRight" || event.key === "ArrowDown") {
      event.preventDefault();
      if (compact) setPopupOpen(true);
      else if (collapsed) { focusChildren.current = true; onToggle(); }
      else children.current?.querySelector<HTMLAnchorElement>("a")?.focus();
    } else if (event.key === "ArrowLeft" && !compact && !collapsed) { event.preventDefault(); onToggle(); }
  };
  return <li className="gateway-sidebar-group">
    <button ref={trigger} type="button" className={`gateway-sidebar-group-toggle${current ? " contains-current" : ""}`} aria-label={group.title} title={compact ? group.title : undefined} aria-expanded={expanded} aria-controls={expanded ? childID : undefined} aria-haspopup={compact ? "dialog" : undefined} onKeyDown={triggerKey} onClick={() => compact ? setPopupOpen((open) => !open) : onToggle()}>
      <Icon data={navigationIcon(group.paths[0])} size={20} />
      <span className={compact ? "gateway-sidebar-sr-only" : "gateway-sidebar-label"}>{group.title}</span>
      <Icon data={expanded ? ChevronDown : ChevronRight} size={16} className="gateway-sidebar-chevron" />
    </button>
    {!compact && expanded && <ul id={childID} ref={children} className="gateway-sidebar-children" onKeyDown={focusLinks}>{group.links.map((link) => <li key={link.route.path}><SidebarLink link={link} /></li>)}</ul>}
    {compact && <Popup open={popupOpen} anchorElement={trigger.current} placement="right-start" offset={8} initialFocus={0} returnFocus={trigger} disableTransition onOpenChange={setPopupOpen} className="gateway-sidebar-popup">
      <div role="dialog" aria-label={`${group.title} navigation`}><h2>{group.title}</h2><ul id={childID} onKeyDown={focusLinks}>{group.links.map((link) => <li key={link.route.path}><SidebarLink link={link} onNavigate={() => setPopupOpen(false)} /></li>)}</ul></div>
    </Popup>}
  </li>;
}

export function SidebarNavigation({ sections, compact }: { sections: VisibleNavigationSection[]; compact: boolean }) {
  const { pathname } = useLocation();
  const activeGroup = activeNavigationGroup(sections, pathname);
  const [collapsed, setCollapsed] = useState(() => {
    const saved = readCollapsedGroups();
    return activeGroup ? { ...saved, [activeGroup]: false } : saved;
  });
  useEffect(() => { if (activeGroup) setCollapsed((saved) => saved[activeGroup] ? { ...saved, [activeGroup]: false } : saved); }, [activeGroup, pathname]);
  useEffect(() => saveCollapsedGroups(collapsed), [collapsed]);
  return <div className={`gateway-sidebar-navigation${compact ? " is-compact" : ""}`}>
    {sections.map((section) => <section key={section.id} aria-labelledby={`sidebar-section-${section.id}`}>
      <h2 id={`sidebar-section-${section.id}`} className={compact ? "gateway-sidebar-sr-only" : "gateway-sidebar-section-title"}>{section.title}</h2>
      <ul>{section.items.map((item) => item.kind === "link" ? <li key={item.route.path}><SidebarLink link={item} compact={compact} /></li> : <SidebarGroup key={item.id} group={item} compact={compact} collapsed={collapsed[item.id]} onToggle={() => setCollapsed((saved) => ({ ...saved, [item.id]: !saved[item.id] }))} />)}</ul>
    </section>)}
  </div>;
}
