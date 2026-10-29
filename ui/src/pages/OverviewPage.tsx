import { useEffect, useState } from 'react'
import { Loading } from '@geist-ui/core'
import { Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { api } from '../api'
import type { MetricPoint, Overview } from '../types'
import { useI18n } from '../i18n'
import { PageHeader } from '../components/PageHeader'
import { Panel } from '../components/Panel'
import { MetricStrip } from '../components/MetricStrip'
import { chartAxis, chartTooltip } from '../components/chartTheme'
import { useGlide } from '../hooks/useGlide'
import { bytes, clock, compact, ms } from '../lib/format'

export function OverviewPage({refresh}:{refresh:number}){
  const {t}=useI18n()
  const [data,setData]=useState<Overview|null>(null)
  // Latency scope: site-wide by default, "me" narrows to the caller's own
  // searches. Switching keeps the current data on screen until the refetch
  // lands; a late response for the previous scope is dropped.
  const [scope,setScope]=useState<'all'|'me'>('all')
  useEffect(()=>{
    let live=true
    api<Overview>(scope==='me'?'/api/v1/overview?scope=me':'/api/v1/overview').then(v=>{if(live)setData(v)})
    return()=>{live=false}
  },[refresh,scope])
  if(!data)return <Loading/>
  return <div className="overview-page">
    <PageHeader title={t('Index overview')} description={t('Current retrieval health across your repositories.')}/>
    <MetricStrip values={[
      [t('Projects count'),data.repositories],
      [t('Indexed files'),compact(data.files)],
      [t('Context chunks'),compact(data.chunks)],
      [t('Storage'),bytes(data.storage_bytes)],
      [t('Search P95'),ms(data.search_p95_ms)],
    ]}/>
    <div className="overview-grid">
      <Panel title={t('Latency profile')} meta={t('LAST 24 HOURS')} actions={<ScopeSwitch scope={scope} onChange={setScope}/>}>
        <LatencyChart points={data.latency_points||[]} empty={scope==='me'?t('No searches of yours in the last 24 hours.'):undefined} summary={[['P50',data.search_p50_ms],['P95',data.search_p95_ms],['P99',data.search_p99_ms]]}/>
      </Panel>
      <Panel title={t('Index health')} meta={`${data.healthy}/${data.repositories} ${t('HEALTHY')}`}>
        <HealthMatrix data={data}/>
      </Panel>
    </div>
  </div>
}

function ScopeSwitch({scope,onChange}:{scope:'all'|'me';onChange:(v:'all'|'me')=>void}){
  const {t}=useI18n()
  const glide=useGlide<HTMLDivElement>('button',[scope],{hover:false})
  return <div className="scope-switch glide" ref={glide.ref} role="group" aria-label={t('Latency scope')}>
    {glide.layer}
    {(['all','me'] as const).map(v=><button key={v} type="button" className={scope===v?'active':''} aria-pressed={scope===v} onClick={()=>onChange(v)}>{t(v==='all'?'Site-wide':'Mine')}</button>)}
  </div>
}

function LatencyChart({points,summary,empty}:{points:MetricPoint[];summary:[string,number][];empty?:string}){
  const {t,language}=useI18n()
  const data=points.map(p=>({time:new Date(p.timestamp).getTime(),value:Math.round(p.value)}))
  const tick=(v:number)=>clock(v,language)
  return <div className="latency-chart">
    <div className="chart-area">
      {data.length===0?<div className="chart-empty">{empty||t('No latency samples yet. Run a search to record one.')}</div>:
      <ResponsiveContainer width="100%" height="100%">
        <AreaChart data={data} margin={{top:10,right:14,left:0,bottom:0}}>
          <defs><linearGradient id="latencyFill" x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stopColor="currentColor" stopOpacity={.16}/><stop offset="100%" stopColor="currentColor" stopOpacity={0}/></linearGradient></defs>
          <CartesianGrid stroke="currentColor" strokeOpacity={.07} vertical={false}/>
          <XAxis dataKey="time" type="number" scale="time" domain={['dataMin','dataMax']} tickFormatter={tick} minTickGap={48} {...chartAxis}/>
          <YAxis tickFormatter={v=>ms(Number(v))} width={58} {...chartAxis}/>
          <Tooltip formatter={(v)=>[ms(Number(v)),t('Latency')]} labelFormatter={(v)=>clock(Number(v),language)} cursor={{stroke:'var(--line-strong)'}} {...chartTooltip}/>
          <Area type="monotone" dataKey="value" stroke="currentColor" strokeWidth={1.5} fill="url(#latencyFill)" dot={data.length<3?{r:3,fill:'currentColor',strokeWidth:0}:false} activeDot={{r:3}} isAnimationActive={false}/>
        </AreaChart>
      </ResponsiveContainer>}
    </div>
    <div className="chart-labels">{summary.map(([label,value])=><div key={label}><span>{label}</span><strong>{ms(value)}</strong></div>)}</div>
  </div>
}

function HealthMatrix({data}:{data:Overview}){
  const {t}=useI18n()
  const coverage=data.chunks?`${Math.round(data.embedded_chunks/data.chunks*100)}%`:'—'
  const rows=[['Healthy',data.healthy,'ok'],['Lagging',data.lagging,'warn'],['Failed jobs',data.failed_jobs,'bad'],['Vector coverage',coverage,'neutral']]
  return <div className="health-matrix">
    {rows.map(([label,value,tone])=><div key={String(label)}><span className={`status-dot ${tone}`}/><span>{t(String(label))}</span><strong>{value}</strong></div>)}
  </div>
}
