import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import type { AceUsageDay } from '../types'
import { useI18n } from '../i18n'
import { chartAxis, chartTooltip } from '../components/chartTheme'
import { compact } from '../lib/format'

// MCP usage pages. Endpoint accents reuse the three-path legend hues
// (retrieval = semantic blue, upload = structural green, enhance = lexical
// amber) so the console keeps one color language.
export const USAGE_ENDPOINTS=['retrieval','upload','enhance'] as const
export const usageColor:Record<string,string>={retrieval:'var(--blue)',upload:'var(--ok)',enhance:'var(--warn)'}
const usageUnitLabel:Record<string,string>={retrieval:'Context tokens served',upload:'Blobs uploaded',enhance:'Output tokens generated'}
export type UsageTotals=Record<string,{calls:number;units:number}>
export function usageTotals(daily:AceUsageDay[]):UsageTotals{
  const out:UsageTotals={}
  for(const e of USAGE_ENDPOINTS)out[e]={calls:0,units:0}
  for(const row of daily){const slot=out[row.endpoint]??(out[row.endpoint]={calls:0,units:0});slot.calls+=row.calls;slot.units+=row.units}
  return out
}
export const usageWindowMeta=(days:number)=>days===7?'LAST 7 DAYS':days===90?'LAST 90 DAYS':'LAST 30 DAYS'
export function RangeSwitch({days,onChange}:{days:number;onChange:(d:number)=>void}){
  const {t}=useI18n()
  return <div className="range-switch" role="group" aria-label={t('Usage window')}>
    {[7,30,90].map(d=><button key={d} type="button" className={days===d?'active':''} onClick={()=>onChange(d)}>{t(`${d}d`)}</button>)}
  </div>
}
export function UsageTrend({daily,days}:{daily:AceUsageDay[];days:number}){
  const {t,language}=useI18n()
  // Skeleton of every calendar day in the window so quiet days render as gaps
  // instead of collapsing the axis; server rows are keyed by their date part.
  const byDay=new Map<string,{day:number;retrieval:number;upload:number;enhance:number}>()
  for(let i=days-1;i>=0;i--){const d=new Date();d.setHours(0,0,0,0);d.setDate(d.getDate()-i);const key=`${d.getFullYear()}-${String(d.getMonth()+1).padStart(2,'0')}-${String(d.getDate()).padStart(2,'0')}`;byDay.set(key,{day:d.getTime(),retrieval:0,upload:0,enhance:0})}
  for(const row of daily){const slot=byDay.get(row.day.slice(0,10));if(slot&&row.endpoint in slot)slot[row.endpoint as 'retrieval'|'upload'|'enhance']+=row.calls}
  const data=[...byDay.values()]
  const label=(v:number)=>new Intl.DateTimeFormat(language==='zh'?'zh-CN':'en',{month:'short',day:'2-digit'}).format(v)
  return <div className="usage-chart">
    <div className="chart-area">
      {daily.length===0?<div className="chart-empty">{t('No MCP calls recorded yet.')}</div>:
      <ResponsiveContainer width="100%" height="100%">
        <BarChart data={data} margin={{top:10,right:14,left:0,bottom:0}} barCategoryGap="28%">
          <CartesianGrid stroke="currentColor" strokeOpacity={.07} vertical={false}/>
          <XAxis dataKey="day" tickFormatter={label} minTickGap={32} {...chartAxis}/>
          <YAxis allowDecimals={false} tickFormatter={v=>compact(Number(v))} width={40} {...chartAxis}/>
          <Tooltip formatter={(v,name)=>[compact(Number(v)),t(String(name))]} labelFormatter={v=>label(Number(v))} cursor={{fill:'var(--surface-2)',opacity:.6}} {...chartTooltip}/>
          {USAGE_ENDPOINTS.map(e=><Bar key={e} dataKey={e} stackId="calls" fill={usageColor[e]} isAnimationActive={false} maxBarSize={26}/>)}
        </BarChart>
      </ResponsiveContainer>}
    </div>
    <div className="usage-legend">{USAGE_ENDPOINTS.map(e=><span key={e}><i className="usage-dot" style={{background:usageColor[e]}}/>{t(e)}</span>)}</div>
  </div>
}
export function EndpointCards({totals}:{totals:UsageTotals}){
  const {t}=useI18n()
  const totalCalls=USAGE_ENDPOINTS.reduce((sum,e)=>sum+totals[e].calls,0)
  return <div className="usage-cards">{USAGE_ENDPOINTS.map(e=>{const share=totalCalls?Math.round(totals[e].calls/totalCalls*100):0;return <div className="usage-card" key={e} style={{'--usage-accent':usageColor[e]} as React.CSSProperties}>
    <header><i className="usage-dot"/><span>{t(e)}</span><small>{share}% {t('of all calls')}</small></header>
    <strong>{compact(totals[e].calls)}</strong>
    <p>{compact(totals[e].units)} {t(usageUnitLabel[e])}</p>
    <div className="usage-share"><span style={{width:`${share}%`}}/></div>
  </div>})}</div>
}
