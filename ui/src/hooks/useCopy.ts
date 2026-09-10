import { useState } from 'react'

// useCopy writes text to the clipboard and flips `copied` for 1.6s so the
// caller can swap its button label/icon; clipboard failures are swallowed.
export function useCopy():[boolean,(text:string)=>Promise<void>]{
  const [copied,setCopied]=useState(false)
  const copy=async(text:string)=>{try{await navigator.clipboard.writeText(text);setCopied(true);window.setTimeout(()=>setCopied(false),1600)}catch{}}
  return [copied,copy]
}
