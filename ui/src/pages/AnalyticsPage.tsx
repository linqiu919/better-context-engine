import { useEffect, useState } from 'react'
import { Button, Loading } from '@geist-ui/core'
import { Area, AreaChart, Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { api } from '../api'
import type { Analytics, AnalyticsHour } from '../types'
import { useI18n } from '../i18n'
import { PageHeader } from '../components/PageHeader'
import { Panel } from '../components/Panel'
import { chartAxis, chartTooltip } from '../components/chartTheme'
import { clock, ms } from '../lib/format'

// Admin analytics: six hourly series over the last 24 hours. The endpoint is
// memoized server-side for 60s, so refreshing is cheap; there is deliberately
// no SSE auto-refresh here — it's a dashboard, not a live feed.
type AnalyticsPoint = AnalyticsHour & { time:number; ingest_mb:number; degraded_pct:number; cache_pct:number }
export function AnalyticsPage(){
  const {t}=useI18n()
  const [data,setData]=useState<Analytics|null>(null)
  const load=()=>api<Analytics>('/api/v1/admin/analytics').then(setData).catch(()=>setData(null))
  useEffect(()=>{void load()},[])
  if(!data)return <Loading/>
  const hours:AnalyticsPoint[]=data.hours.map(h=>({...h,time:new Date(h.hour).getTime(),ingest_mb:Math.round(h.ingest_bytes/1024/1024*10)/10,degraded_pct:Math.round(h.degraded_rate*1000)/10,cache_pct:Math.round(h.cache_rate*1000)/10}))
  const pct=(v:number)=>`${v}%`
  return <>
    <PageHeader title={t('Analytics')} description={t('Hourly retrieval and ingestion telemetry across all users; the data refreshes at most once a minute.')} actions={<Button auto scale={.8} onClick={()=>{setData(null);void load()}}>{t('Refresh')}</Button>}/>
    <div className="analytics-grid">
      <Panel title={t('Latency (retrieval stage)')} meta={t('ALL USERS · LAST 24 HOURS')}>
        <HourChart data={hours} kind="area" dataKey="p95_ms" fmt={v=>ms(v)} label={t('Hourly P95')}/>
      </Panel>
      <Panel title={t('Degraded rate')} meta={`${data.total_degraded}/${data.total_searches} ${t('DEGRADED')}`}>
        <HourChart data={hours} kind="area" dataKey="degraded_pct" fmt={pct} label={t('Degraded rate')}/>
      </Panel>
      <Panel title={t('Search volume')} meta={`${data.total_requests} ${t('REQUESTS · 24H')}`}>
        <HourChart data={hours} kind="bar" dataKey="requests" fmt={v=>String(v)} label={t('Requests / hour')}/>
      </Panel>
      <Panel title={t('Ingest volume')} meta={`${data.total_ingest_mb} MB · 24H`}>
        <HourChart data={hours} kind="bar" dataKey="ingest_mb" fmt={v=>`${v} MB`} label={t('Uploaded / hour')}/>
      </Panel>
      <Panel title={t('Cache hit rate')} meta={`${Math.round(data.cache_hit_rate*100)}% ${t('OVERALL')}`}>
        <HourChart data={hours} kind="area" dataKey="cache_pct" fmt={pct} label={t('Cache hit rate')}/>
      </Panel>
      <Panel title={t('First-query embedding debt')} meta={t('AVG MS PER HOUR')}>
        <HourChart data={hours} kind="area" dataKey="inline_avg_ms" fmt={v=>ms(v)} label={t('Avg inline embed')}/>
      </Panel>
    </div>
  </>
}
function HourChart({data,kind,dataKey,fmt,label}:{data:AnalyticsPoint[];kind:'area'|'bar';dataKey:keyof AnalyticsPoint;fmt:(v:number)=>string;label:string}){
  const {t,language}=useI18n()
  const tick=(v:number)=>clock(v,language)
  const has=data.some(d=>Number(d[dataKey]??0)>0)
  return <div className="latency-chart"><div className="chart-area">
    {!has?<div className="chart-empty">{t('No samples in this window yet.')}</div>:
    <ResponsiveContainer width="100%" height="100%">
      {kind==='area'?
      <AreaChart data={data} margin={{top:10,right:14,left:0,bottom:0}}>
        <defs><linearGradient id={`fill-${String(dataKey)}`} x1="0" y1="0" x2="0" y2="1"><stop offset="0%" stopColor="currentColor" stopOpacity={.16}/><stop offset="100%" stopColor="currentColor" stopOpacity={0}/></linearGradient></defs>
        <CartesianGrid stroke="currentColor" strokeOpacity={.07} vertical={false}/>
        <XAxis dataKey="time" type="number" scale="time" domain={['dataMin','dataMax']} tickFormatter={tick} minTickGap={44} {...chartAxis}/>
        <YAxis tickFormatter={v=>fmt(Number(v))} width={56} {...chartAxis}/>
        <Tooltip formatter={v=>[fmt(Number(v)),label]} labelFormatter={v=>clock(Number(v),language)} cursor={{stroke:'var(--line-strong)'}} {...chartTooltip}/>
        <Area type="monotone" dataKey={String(dataKey)} stroke="currentColor" strokeWidth={1.5} fill={`url(#fill-${String(dataKey)})`} dot={false} activeDot={{r:3}} isAnimationActive={false}/>
      </AreaChart>
      :
      <BarChart data={data} margin={{top:10,right:14,left:0,bottom:0}}>
        <CartesianGrid stroke="currentColor" strokeOpacity={.07} vertical={false}/>
        <XAxis dataKey="time" type="number" scale="time" domain={['dataMin','dataMax']} tickFormatter={tick} minTickGap={44} {...chartAxis}/>
        <YAxis tickFormatter={v=>fmt(Number(v))} width={56} {...chartAxis}/>
        <Tooltip formatter={v=>[fmt(Number(v)),label]} labelFormatter={v=>clock(Number(v),language)} cursor={{fill:'var(--line)',fillOpacity:.35}} {...chartTooltip}/>
        <Bar dataKey={String(dataKey)} fill="currentColor" fillOpacity={.55} radius={[3,3,0,0]} isAnimationActive={false}/>
      </BarChart>}
    </ResponsiveContainer>}
  </div></div>
}
