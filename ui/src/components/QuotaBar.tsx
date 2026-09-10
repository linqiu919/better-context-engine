import { compact } from '../lib/format'

// QuotaBar renders one allowance as a labelled progress bar; tones reuse the
// three-path legend colors (retrieval=blue / enhance=warn / upload=ok) with a
// neutral tone for storage, and the fill turns red once the limit is reached.
export function QuotaBar({label,used,limit,tone,format}:{label:string;used:number;limit:number;tone:'blue'|'warn'|'ok'|'neutral';format?:(v:number)=>string}){
  const fmt=format||((v:number)=>compact(v))
  const pct=limit>0?Math.min(100,used/limit*100):0
  return <div className={`quota-bar${used>=limit?' over':''}`}>
    <div className="quota-bar-head"><span>{label}</span><strong>{fmt(used)} / {fmt(limit)}</strong></div>
    <div className="quota-track"><i className={`quota-fill ${tone}`} style={{width:`${pct}%`}}/></div>
  </div>
}
