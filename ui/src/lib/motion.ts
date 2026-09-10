// prefersReducedMotion mirrors the CSS media query so scripted scrolling and
// copy rotation can degrade the same way the stylesheet does.
export const prefersReducedMotion=()=>window.matchMedia('(prefers-reduced-motion: reduce)').matches
