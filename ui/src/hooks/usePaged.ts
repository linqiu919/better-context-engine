import { useState } from 'react'

export const PAGE_SIZE=10

// usePaged slices a client-side list into pages (default 10); the page clamps
// automatically when the list shrinks (filter, refresh) below the current one.
export function usePaged<T>(items:T[],size=PAGE_SIZE):{page:number;setPage:(p:number)=>void;paged:T[];total:number;size:number}{
  const [page,setPage]=useState(1)
  const total=items.length
  const current=Math.min(page,Math.max(1,Math.ceil(total/size)))
  return {page:current,setPage,paged:items.slice((current-1)*size,current*size),total,size}
}
