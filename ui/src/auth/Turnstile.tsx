import { useEffect, useRef } from 'react'

declare global { interface Window { turnstile?:{ render:(el:HTMLElement,opts:Record<string,unknown>)=>string; reset:(id:string)=>void; remove:(id:string)=>void } } }

// Turnstile renders the Cloudflare widget (script loaded on demand) and
// reports tokens upward; bump resetSignal to request a fresh token after a
// token has been consumed by the server.
export function Turnstile({siteKey,dark,onToken,resetSignal}:{siteKey:string;dark:boolean;onToken:(token:string)=>void;resetSignal:number}){
  const ref=useRef<HTMLDivElement>(null)
  const widget=useRef<string|null>(null)
  const cb=useRef(onToken)
  cb.current=onToken
  useEffect(()=>{
    let cancelled=false
    const render=()=>{
      if(cancelled||!ref.current||!window.turnstile||widget.current!==null)return
      widget.current=window.turnstile.render(ref.current,{sitekey:siteKey,theme:dark?'dark':'light',callback:(token:string)=>cb.current(token),'expired-callback':()=>cb.current(''),'error-callback':()=>cb.current('')})
    }
    if(window.turnstile)render()
    else{
      let script=document.querySelector<HTMLScriptElement>('script[data-turnstile]')
      if(!script){
        script=document.createElement('script')
        script.src='https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit'
        script.async=true
        script.dataset.turnstile='1'
        document.head.appendChild(script)
      }
      script.addEventListener('load',render)
    }
    return()=>{cancelled=true;if(widget.current!==null&&window.turnstile){window.turnstile.remove(widget.current);widget.current=null}}
  },[siteKey,dark])
  useEffect(()=>{if(resetSignal>0&&widget.current!==null&&window.turnstile){window.turnstile.reset(widget.current);cb.current('')}},[resetSignal])
  return <div className="turnstile-box" ref={ref}/>
}
