import { useEffect, useRef, useState } from 'react'

// useCopy writes text to the clipboard and flips `copied` for 1.6s so the
// caller can swap its button label/icon; clipboard failures are swallowed.
// A repeat copy restarts the 1.6s window and the timer dies with the caller.
export function useCopy():[boolean,(text:string)=>Promise<void>]{
  const [copied,setCopied]=useState(false)
  const timer=useRef<number>()
  useEffect(()=>()=>window.clearTimeout(timer.current),[])
  const copy=async(text:string)=>{try{await navigator.clipboard.writeText(text);setCopied(true);window.clearTimeout(timer.current);timer.current=window.setTimeout(()=>setCopied(false),1600)}catch{}}
  return [copied,copy]
}
