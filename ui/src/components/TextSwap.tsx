import { useRef, type CSSProperties } from 'react'

// TextSwap replaces a label character by character ("文字替换"): when the text
// changes, the new one rises in with a light per-character stagger. Only a
// real change animates — the first render is static, and re-renders with the
// same text keep whatever the current text mounted with.
export function TextSwap({text}:{text:string}){
  const shown=useRef<{text:string;swap:boolean}>()
  if(shown.current?.text!==text)shown.current={text,swap:shown.current!==undefined}
  if(!shown.current.swap)return <span className="text-swap">{text}</span>
  return <span key={text} className="text-swap swapping">
    <span className="sr-only">{text}</span>
    {[...text].map((c,i)=><span key={i} aria-hidden="true" style={{'--i':i} as CSSProperties}>{c}</span>)}
  </span>
}
