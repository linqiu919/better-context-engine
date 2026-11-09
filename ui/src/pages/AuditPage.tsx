import { Loading } from '@geist-ui/core'
import { api } from '../api'
import type { AuditEvent } from '../types'
import { useI18n } from '../i18n'
import { PageHeader } from '../components/PageHeader'
import { Status } from '../components/Status'
import { Empty } from '../components/Empty'
import { Pagination } from '../components/Pagination'
import { usePaged } from '../hooks/usePaged'
import { formatDate, short } from '../lib/format'
import { useResource } from '../hooks/useResource'
import { LoadError } from '../components/LoadError'

export function AuditPage(){
  const {t,language}=useI18n()
  const list=useResource(()=>api<{events:AuditEvent[]}>('/api/v1/admin/audit-log').then(v=>v.events||[]),[])
  const events=list.data
  const {page,setPage,paged,total}=usePaged(events||[])
  if(!events)return list.error?<LoadError error={list.error} onRetry={list.reload}/>:<Loading/>
  return <>
    <PageHeader title={t('Audit log')} description={t('Immutable record of authentication and privileged actions.')}/>
    <div className="data-table-wrap" role="region" tabIndex={0}>
      <table className="data-table audit-table">
        <thead><tr><th>{t('Time')}</th><th>{t('Actor')}</th><th>{t('Action')}</th><th>{t('Target')}</th><th>{t('Result')}</th></tr></thead>
        <tbody>{paged.map(event=><tr key={event.id}>
          <td>{formatDate(event.created_at,language)}</td>
          <td>{event.actor}</td>
          <td><code className="inline-code">{event.action}</code></td>
          <td>{event.target_type} / <code>{short(event.target_id)}</code></td>
          <td><Status value={event.result}/></td>
        </tr>)}</tbody>
      </table>
      {events.length===0&&<Empty title={t('No audit events')} text={t('Privileged actions will be recorded here.')}/>}
    </div>
    <Pagination total={total} page={page} onChange={setPage}/>
  </>
}
