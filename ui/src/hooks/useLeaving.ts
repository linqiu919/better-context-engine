import { useLayoutEffect, useRef, useState } from 'react'
import { prefersReducedMotion } from '../lib/motion'

const FADE=180

// useLeaving runs the delete choreography from "添加和删除的细节": the removed
// row fades out in place first (opacity leads on removal), then is dropped and
// the rows below slide up to close the gap; a row that newly enters the list
// (e.g. pulled in from the next page) fades in after the slide (opacity trails
// on addition). Rows carry `data-key`; `ref` goes on their container.
export function useLeaving<T extends HTMLElement>(){
  const ref=useRef<T>(null)
  const [leaving,setLeaving]=useState<string|null>(null)
  const before=useRef<Map<string,number>|null>(null)
  const rows=()=>[...ref.current?.querySelectorAll<HTMLElement>('[data-key]')??[]]
  useLayoutEffect(()=>{
    const prev=before.current
    if(!prev)return
    before.current=null
    for(const el of rows()){
      const from=prev.get(el.dataset.key!)
      if(from==null){el.animate([{opacity:0},{opacity:1}],{duration:200,delay:160,easing:'ease-out',fill:'backwards'});continue}
      const dy=from-el.getBoundingClientRect().top
      if(dy)el.animate([{transform:`translateY(${dy}px)`},{transform:'none'}],{duration:280,easing:'cubic-bezier(.16,1,.3,1)'})
    }
  })
  const leave=(key:string,drop:()=>void)=>{
    if(prefersReducedMotion()){drop();return}
    setLeaving(key)
    window.setTimeout(()=>{
      before.current=new Map(rows().map(el=>[el.dataset.key!,el.getBoundingClientRect().top]))
      drop();setLeaving(null)
    },FADE)
  }
  return {ref,leaving,leave}
}
