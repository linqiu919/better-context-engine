import { useLayoutEffect, useRef, useState, type CSSProperties, type DependencyList, type PointerEvent } from 'react'

type Box={x:number;y:number;w:number;h:number}
// on = visible; snap = jump without sliding (first appearance after hiding,
// or the first measurement) so the pill never sweeps in from a stale spot.
type Pill={box:Box|null;on:boolean;snap:boolean}
type Tip={x:number;label:string;on:boolean;snap:boolean}

const TIP_DELAY=480
const TIP_GRACE=200

// useGlide draws one shared highlight per group ("跳跃"): instead of every item
// owning its hover/active background, a single pill slides between the items
// matched by `item`, so a nav, pager or tab strip reads as one control. With
// `tip`, the group also shares one tooltip built from the hovered item's
// aria-label: only the first hover waits, moving along the group swaps it in
// place, and leaving keeps it for a short grace period.
// The container takes `ref` + `handlers`, gets the `glide` class, and renders
// `layer` first; active items carry the `active` class.
export function useGlide<T extends HTMLElement>(item:string,deps:DependencyList,{hover=true,tip=false}:{hover?:boolean;tip?:boolean}={}){
  const ref=useRef<T>(null)
  const [active,setActive]=useState<Pill>({box:null,on:false,snap:true})
  const [over,setOver]=useState<Pill>({box:null,on:false,snap:true})
  const [label,setLabel]=useState<Tip>({x:0,label:'',on:false,snap:true})
  const timer=useRef(0)
  const tipOn=useRef(false)
  const measure=(el:Element):Box=>{
    const host=ref.current!
    const a=el.getBoundingClientRect(),b=host.getBoundingClientRect()
    return {x:a.left-b.left+host.scrollLeft-host.clientLeft,y:a.top-b.top+host.scrollTop-host.clientTop,w:a.width,h:a.height}
  }
  useLayoutEffect(()=>{
    const host=ref.current;if(!host)return
    const sync=()=>{
      const el=host.querySelector(`:is(${item}).active`)
      setActive(p=>{
        if(!el)return !p.on&&p.snap?p:{...p,on:false,snap:true}
        const box=measure(el)
        // Unchanged geometry keeps the same state object, so resize callbacks
        // (fired on every observed item) don't re-render the host page.
        if(p.on&&p.box&&p.box.x===box.x&&p.box.y===box.y&&p.box.w===box.w&&p.box.h===box.h)return p
        return {box,on:true,snap:!p.on}
      })
    }
    sync()
    const ro=new ResizeObserver(sync)
    ro.observe(host);host.querySelectorAll(item).forEach(el=>ro.observe(el))
    return()=>ro.disconnect()
  // eslint-disable-next-line react-hooks/exhaustive-deps
  },deps)
  useLayoutEffect(()=>()=>window.clearTimeout(timer.current),[])
  const onPointerOver=(e:PointerEvent)=>{
    if(e.pointerType==='touch')return
    const el=(e.target as Element).closest(item)
    if(!el||!ref.current?.contains(el)||(el as HTMLButtonElement).disabled)return
    const box=measure(el)
    if(hover)setOver(p=>({box,on:true,snap:!p.on}))
    if(!tip)return
    window.clearTimeout(timer.current)
    const next={x:box.x+box.w/2,label:el.getAttribute('aria-label')||''}
    if(tipOn.current){setLabel({...next,on:true,snap:false});return}
    setLabel({...next,on:false,snap:true})
    timer.current=window.setTimeout(()=>{tipOn.current=true;setLabel(q=>({...q,on:true}))},TIP_DELAY)
  }
  const onPointerLeave=()=>{
    setOver(p=>({...p,on:false}))
    if(!tip)return
    window.clearTimeout(timer.current)
    timer.current=window.setTimeout(()=>{tipOn.current=false;setLabel(p=>({...p,on:false}))},TIP_GRACE)
  }
  const pill=(cls:string,p:Pill)=>p.box&&<span aria-hidden="true" className={`glide-pill ${cls}`} data-on={p.on||undefined} data-snap={p.snap||undefined}
    style={{'--gx':`${p.box.x}px`,'--gy':`${p.box.y}px`,'--gw':`${p.box.w}px`,'--gh':`${p.box.h}px`} as CSSProperties}/>
  const layer=<>
    {hover&&pill('glide-hover',over)}
    {pill('glide-active',active)}
    {tip&&label.label&&<span aria-hidden="true" className="glide-tip" data-on={label.on||undefined} data-snap={label.snap||undefined} style={{'--tx':`${label.x}px`} as CSSProperties}>{label.label}</span>}
  </>
  return {ref,layer,handlers:{onPointerOver,onPointerLeave}}
}
