import { useLayoutEffect, useState } from 'react'
import type { RefObject } from 'react'

// useFitRows picks a page size that fills the viewport without scrolling: the
// rows that fit between the table body's top and the bottom of the scrolling
// .main pane, after reserving whatever follows the table in the page (the
// pagination bar and the page padding). Row height is the tallest rendered
// row, so branch rows never push the page into a scrollbar. Re-measures on
// window resize and whenever `key` changes (data loaded or filtered); until
// rows exist to measure it returns `min`.
export function useFitRows(body:RefObject<HTMLTableSectionElement>,key:unknown,min=5,max=15):number{
  const [rows,setRows]=useState(min)
  useLayoutEffect(()=>{
    const measure=()=>{
      const el=body.current
      const main=el?.closest<HTMLElement>('.main')
      const page=el?.closest<HTMLElement>('.page-container')
      const wrap=el?.closest<HTMLElement>('.data-table-wrap')
      const rowHeight=Math.max(0,...[...el?.rows??[]].map(r=>r.offsetHeight))
      if(!el||!main||!page||!wrap||!rowHeight)return
      const below=page.getBoundingClientRect().bottom-wrap.getBoundingClientRect().bottom
      const top=el.getBoundingClientRect().top+main.scrollTop
      const fit=Math.floor((main.getBoundingClientRect().bottom-top-below)/rowHeight)
      setRows(Math.min(max,Math.max(min,fit)))
    }
    measure()
    window.addEventListener('resize',measure)
    return ()=>window.removeEventListener('resize',measure)
  },[key,min,max])
  return rows
}
