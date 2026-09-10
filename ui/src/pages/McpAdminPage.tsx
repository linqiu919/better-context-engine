import { useEffect, useState } from 'react'
import { Input, Loading } from '@geist-ui/core'
import { api } from '../api'
import type { AceUsage, AceUsageDay } from '../types'
import { useI18n } from '../i18n'
import { PageHeader } from '../components/PageHeader'
import { Panel } from '../components/Panel'
import { MetricStrip } from '../components/MetricStrip'
import { Pagination } from '../components/Pagination'
import { usePaged } from '../hooks/usePaged'
import { compact, formatDate } from '../lib/format'
import { RangeSwitch, USAGE_ENDPOINTS, UsageTrend, usageColor, usageTotals, usageWindowMeta } from './usageShared'

// Per-user view is merged client-side: the API returns one row per
// (user, endpoint); rows fold into one line per user with the three endpoint
// cells, sorted by total calls. Search and paging are client-side too.
type MergedUsage={user_id:string;username:string;last_day:string;per:Record<string,{calls:number;units:number}>}
export function McpAdminPage(){
  const {t,language}=useI18n()
  const [days,setDays]=useState(7)
  const [query,setQuery]=useState('')
  const [data,setData]=useState<{usage:AceUsage[];daily:AceUsageDay[]}|null>(null)
  useEffect(()=>{api<{usage:AceUsage[];daily:AceUsageDay[]}>(`/api/v1/admin/ace-usage?days=${days}`).then(v=>setData({usage:v.usage||[],daily:v.daily||[]})).catch(()=>setData({usage:[],daily:[]}))},[days])
  const merged=(()=>{
    const by=new Map<string,MergedUsage>()
    for(const r of data?.usage||[]){
      const k=r.user_id||r.username
      const e=by.get(k)||{user_id:r.user_id,username:r.username,last_day:r.last_day,per:{}}
      e.per[r.endpoint]={calls:r.calls,units:r.units}
      if(r.last_day>e.last_day)e.last_day=r.last_day
      by.set(k,e)
    }
    return [...by.values()].sort((a,b)=>USAGE_ENDPOINTS.reduce((s,e)=>s+(b.per[e]?.calls||0)-(a.per[e]?.calls||0),0))
  })()
  const q=query.trim().toLowerCase()
  const visible=q?merged.filter(r=>(r.username==='shared-token'?t('shared-token'):r.username).toLowerCase().includes(q)):merged
  const pager=usePaged(visible)
  if(!data)return <Loading/>
  const totals=usageTotals(data.daily)
  const totalCalls=USAGE_ENDPOINTS.reduce((s,e)=>s+totals[e].calls,0)
  const accounts=new Set(data.usage.filter(r=>r.user_id).map(r=>r.user_id)).size
  const endpointCell=(r:MergedUsage,e:string)=>{const v=r.per[e];return v?<span className="usage-chip"><i className="usage-dot" style={{background:usageColor[e]||'var(--muted)'}}/>{compact(v.calls)} · {compact(v.units)}</span>:<>—</>}
  return <div className="usage-page">
    <PageHeader title={t('MCP management')} description={t('MCP traffic and trends across all accounts.')} actions={<RangeSwitch days={days} onChange={setDays}/>}/>
    <MetricStrip values={[
      [t('Total calls'),compact(totalCalls)],
      [t('Active accounts'),accounts],
      [t('Retrieval tokens'),compact(totals.retrieval.units)],
      [t('Upload blobs'),compact(totals.upload.units)],
      [t('Enhance tokens'),compact(totals.enhance.units)],
    ]}/>
    <Panel title={t('Daily activity')} meta={t(usageWindowMeta(days))}><UsageTrend daily={data.daily} days={days}/></Panel>
    <Panel title={t('Per-user usage')} meta={t(usageWindowMeta(days))}>
      {data.usage.length===0?<p className="settings-intro">{t('No MCP calls recorded yet.')}</p>:<>
        <div className="usage-filter-row">
          <p className="settings-intro">{t('Cells show calls · units. Units: retrieval = context tokens served, upload = blobs, enhance = output tokens.')}</p>
          <Input crossOrigin="" scale={0.75} clearable placeholder={t('Filter by username')} aria-label={t('Filter by username')} value={query} onChange={e=>setQuery(e.target.value)}/>
        </div>
        {visible.length===0?<p className="settings-intro">{t('No matching users.')}</p>:<>
          <div className="data-table-wrap" role="region" tabIndex={0}>
            <table className="data-table">
              <thead><tr><th>{t('User')}</th><th>{t('retrieval')}</th><th>{t('upload')}</th><th>{t('enhance')}</th><th>{t('Last day')}</th></tr></thead>
              <tbody>{pager.paged.map(row=><tr key={row.user_id||row.username}>
                <td><div className="user-cell"><span className="avatar">{(row.username==='shared-token'?'ST':row.username.slice(0,2)).toUpperCase()}</span><strong>{row.username==='shared-token'?t('shared-token'):row.username}</strong></div></td>
                <td>{endpointCell(row,'retrieval')}</td>
                <td>{endpointCell(row,'upload')}</td>
                <td>{endpointCell(row,'enhance')}</td>
                <td>{formatDate(row.last_day,language)}</td>
              </tr>)}</tbody>
            </table>
          </div>
          <Pagination total={pager.total} page={pager.page} onChange={pager.setPage}/>
        </>}
      </>}
    </Panel>
  </div>
}
