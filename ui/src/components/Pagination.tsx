import { useI18n } from '../i18n'
import { PAGE_SIZE } from '../hooks/usePaged'

function pageNumbers(pages:number,page:number):number[]{
  if(pages<=7)return Array.from({length:pages},(_,i)=>i+1)
  const set=new Set([1,pages,page-1,page,page+1].filter(p=>p>=1&&p<=pages))
  const out:number[]=[]
  let prev=0
  for(const p of [...set].sort((a,b)=>a-b)){
    if(prev&&p-prev>1)out.push(-1)
    out.push(p);prev=p
  }
  return out
}

export function Pagination({total,page,onChange,size=PAGE_SIZE}:{total:number;page:number;onChange:(p:number)=>void;size?:number}){
  const {t}=useI18n()
  const pages=Math.ceil(total/size)
  if(total<=size)return null
  return <div className="pagination">
    <span>{t('{from}–{to} of {total}',{from:(page-1)*size+1,to:Math.min(total,page*size),total})}</span>
    <div className="pagination-controls">
      <button disabled={page<=1} onClick={()=>onChange(page-1)} aria-label={t('Previous page')}>‹</button>
      {pageNumbers(pages,page).map((p,i)=>p<0?<span key={`gap${i}`} className="pagination-gap">…</span>:<button key={p} className={p===page?'active':''} aria-current={p===page?'page':undefined} onClick={()=>onChange(p)}>{p}</button>)}
      <button disabled={page>=pages} onClick={()=>onChange(page+1)} aria-label={t('Next page')}>›</button>
    </div>
  </div>
}
