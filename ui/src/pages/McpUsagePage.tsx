import { useEffect, useState } from 'react'
import { Loading } from '@geist-ui/core'
import { api } from '../api'
import type { AceUsageDay } from '../types'
import { useI18n } from '../i18n'
import { PageHeader } from '../components/PageHeader'
import { Panel } from '../components/Panel'
import { MetricStrip } from '../components/MetricStrip'
import { Empty } from '../components/Empty'
import { compact } from '../lib/format'
import { EndpointCards, RangeSwitch, USAGE_ENDPOINTS, UsageTrend, usageTotals, usageWindowMeta } from './usageShared'

export function McpUsagePage(){
  const {t}=useI18n()
  const [days,setDays]=useState(7)
  const [daily,setDaily]=useState<AceUsageDay[]|null>(null)
  useEffect(()=>{api<{daily:AceUsageDay[]}>(`/api/v1/me/ace-usage?days=${days}`).then(v=>setDaily(v.daily||[])).catch(()=>setDaily([]))},[days])
  if(!daily)return <Loading/>
  const totals=usageTotals(daily)
  const totalCalls=USAGE_ENDPOINTS.reduce((s,e)=>s+totals[e].calls,0)
  const activeDays=new Set(daily.filter(r=>r.calls>0).map(r=>r.day.slice(0,10))).size
  return <div className="usage-page">
    <PageHeader title={t('MCP usage')} description={t('Your MCP / BCE client traffic for this account.')} actions={<RangeSwitch days={days} onChange={setDays}/>}/>
    {totalCalls===0?<Empty title={t('No MCP traffic yet.')} text={t('Connect your MCP client and run a search to see usage here.')}/>:<>
      <MetricStrip values={[
        [t('Total calls'),compact(totalCalls)],
        [t('Retrieval tokens'),compact(totals.retrieval.units)],
        [t('Upload blobs'),compact(totals.upload.units)],
        [t('Enhance tokens'),compact(totals.enhance.units)],
        [t('Active days'),activeDays],
      ]}/>
      <EndpointCards totals={totals}/>
      <Panel title={t('Daily activity')} meta={t(usageWindowMeta(days))}><UsageTrend daily={daily} days={days}/></Panel>
    </>}
  </div>
}
