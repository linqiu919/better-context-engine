import { useCallback, useEffect, useRef, useState } from 'react'
import type { Dispatch, SetStateAction } from 'react'

export type Resource<T>={data:T|null;error:string;loading:boolean;reload:()=>void;setData:Dispatch<SetStateAction<T|null>>}

// useResource owns one fetch lifecycle: loading → ok | error. The last good
// data stays on screen while a reload is in flight or after it fails (so a
// refresh never blanks the page and a transient error never reads as "empty"),
// a sequence number drops responses overtaken by a newer request (fast filter
// or range switches) and nothing is set after unmount. `deps` re-run the load,
// like an effect; `reload` re-runs it on demand (retry buttons, SSE refresh).
export function useResource<T>(load:()=>Promise<T>,deps:unknown[]):Resource<T>{
  const [state,setState]=useState<{data:T|null;error:string;loading:boolean}>({data:null,error:'',loading:true})
  const seq=useRef(0)
  const loader=useRef(load)
  loader.current=load
  useEffect(()=>()=>{seq.current++},[])
  const reload=useCallback(()=>{
    const id=++seq.current
    setState(s=>({...s,loading:true}))
    loader.current().then(
      data=>{if(id===seq.current)setState({data,error:'',loading:false})},
      e=>{if(id===seq.current)setState(s=>({...s,error:e instanceof Error?e.message:String(e),loading:false}))})
  },[])
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(reload,deps)
  const setData=useCallback<Dispatch<SetStateAction<T|null>>>(v=>setState(s=>({...s,data:typeof v==='function'?(v as (p:T|null)=>T|null)(s.data):v})),[])
  return {...state,reload,setData}
}
