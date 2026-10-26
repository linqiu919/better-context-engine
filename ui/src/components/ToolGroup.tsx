import type { ReactNode } from 'react'
import { useGlide } from '../hooks/useGlide'

// ToolGroup wraps a row of icon buttons (console top bar, public glass nav) so
// they share one hover fill and one tooltip that glide between them — the
// buttons read as a single toolbar instead of separate chips. Tooltip text
// comes from each button's aria-label, so members carry no native title.
export function ToolGroup({children}:{children:ReactNode}){
  const glide=useGlide<HTMLDivElement>('.icon-button, .language-switch',[],{tip:true})
  return <div className="tool-group glide" ref={glide.ref} {...glide.handlers}>{glide.layer}{children}</div>
}
