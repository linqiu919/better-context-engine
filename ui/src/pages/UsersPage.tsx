import { useEffect, useState } from 'react'
import { Button, Input, Loading } from '@geist-ui/core'
import { api, jsonBody } from '../api'
import type { QuotaSettings, User } from '../types'
import { useI18n } from '../i18n'
import { PageHeader } from '../components/PageHeader'
import { Status } from '../components/Status'
import { Empty } from '../components/Empty'
import { Pagination } from '../components/Pagination'
import { ConfirmModal } from '../components/ConfirmModal'
import { usePaged } from '../hooks/usePaged'
import { bytes, formatDate } from '../lib/format'
import { useResource } from '../hooks/useResource'
import { LoadError } from '../components/LoadError'

// UsersPage (admin): accounts with today's usage against the shared limits
// returned alongside the list; filter and paging are client-side.
export function UsersPage(){
  const {t,language}=useI18n()
  const list=useResource(()=>api<{users:User[];limits:QuotaSettings}>('/api/v1/admin/users'),[])
  const users=list.data?.users??null,limits=list.data?.limits??null
  const [query,setQuery]=useState('')
  const q=query.trim().toLowerCase()
  const visible=q?(users||[]).filter(u=>u.username.toLowerCase().includes(q)||(u.linuxdo_username||'').toLowerCase().includes(q)):(users||[])
  const userPager=usePaged(visible,5)
  useEffect(()=>userPager.setPage(1),[q])
  const load=list.reload
  // One enable/disable at a time per row; a failure is shown, not swallowed.
  const [toggling,setToggling]=useState<string|null>(null)
  const [toggleError,setToggleError]=useState('')
  const toggle=async(user:User)=>{
    if(toggling)return
    setToggling(user.id);setToggleError('')
    try{await api(`/api/v1/admin/users/${user.id}`,{method:'PATCH',...jsonBody({disabled:!user.disabled})});load()}
    catch(e){setToggleError(`${user.username}: ${t((e as Error).message)}`)}
    finally{setToggling(null)}
  }
  const [pendingReset,setPendingReset]=useState<User|null>(null)
  const [resetting,setResetting]=useState(false)
  const [resetError,setResetError]=useState('')
  const confirmReset=async()=>{
    if(!pendingReset||resetting)return
    setResetting(true);setResetError('')
    try{await api(`/api/v1/admin/users/${pendingReset.id}/reset-quota`,{method:'POST'});setPendingReset(null);load()}
    catch(e){setResetError((e as Error).message)}
    finally{setResetting(false)}
  }
  if(!users)return list.error?<LoadError error={list.error} onRetry={list.reload}/>:<Loading/>
  return <>
    <PageHeader title={t('User management')} description={t('Local accounts, repository ownership and retrieval usage.')}
      actions={<Input crossOrigin="" scale={0.75} clearable placeholder={t('Filter by username')} aria-label={t('Filter by username')} value={query} onChange={e=>setQuery(e.target.value)}/>}/>
    {list.error&&<LoadError error={list.error} onRetry={list.reload}/>}
    {toggleError&&<p className="form-error">{toggleError}</p>}
    <div className="data-table-wrap" role="region" tabIndex={0}>
      <table className="data-table users-table">
        <thead><tr>
          <th>{t('User')}</th>
          <th>{t('Email')}</th>
          <th>{t('Role')}</th>
          <th>{t('Status')}</th>
          <th>{t('Repositories')}</th>
          <th>{t('Storage')}</th>
          <th>{t('Today usage')}</th>
          <th>{t('Last active')}</th>
          <th/>
        </tr></thead>
        <tbody>{userPager.paged.map(user=><tr key={user.id}>
          <td><div className="user-cell">
            <span className="avatar">{user.avatar_url?<img src={user.avatar_url} alt=""/>:user.username.slice(0,2).toUpperCase()}</span>
            <strong>{user.username}</strong>
            {user.linuxdo_id!=null&&<span className="linuxdo-badge" title={`LinuxDo: ${user.linuxdo_username||''} · Lv${user.trust_level||0}`}><img src="/linuxdo.svg" alt="LinuxDo"/>Lv{user.trust_level||0}</span>}
          </div></td>
          <td>{user.email?<span className="user-email" title={user.email}>{user.email}</span>:<span className="quota-exempt">—</span>}</td>
          <td><code className="inline-code">{t(user.role)}</code></td>
          <td><Status value={user.disabled?'disabled':'healthy'}/></td>
          <td>{user.repositories||0}</td>
          <td>{user.role==='admin'||!limits?bytes(user.storage_bytes||0):`${bytes(user.storage_bytes||0)} / ${limits.storage_limit_mb} MB`}</td>
          <td>{user.role==='admin'||!limits
            ?<span className="quota-exempt">{t('Unlimited')}</span>
            :<div className="usage-mini">
              <span title={t('Retrieval today')}><i className="dot-blue"/>{user.retrieval_today||0}/{limits.daily_retrieval}</span>
              <span title={t('Enhance today')}><i className="dot-warn"/>{user.enhance_today||0}/{limits.daily_enhance}</span>
              <span title={t('Uploads today')}><i className="dot-ok"/>{user.upload_today||0}/{limits.daily_upload}</span>
            </div>}</td>
          <td>{formatDate(user.last_active_at,language)}</td>
          <td><div className="row-actions">
            {user.role!=='admin'&&<Button auto scale={.62} ghost onClick={()=>{setPendingReset(user);setResetError('')}}>{t('Reset')}</Button>}
            {user.role!=='admin'&&<Button auto scale={.62} ghost loading={toggling===user.id} disabled={!!toggling&&toggling!==user.id} onClick={()=>toggle(user)}>{t(user.disabled?'Enable':'Disable')}</Button>}
          </div></td>
        </tr>)}</tbody>
      </table>
      {visible.length===0&&<Empty title={t('No matching users.')} text={t('Try a different keyword.')}/>}
    </div>
    <Pagination total={userPager.total} page={userPager.page} onChange={userPager.setPage} size={userPager.size}/>
    {pendingReset&&<ConfirmModal title={t('Reset quota')} target={pendingReset.username} text={t("Reset today's counters for this user?")} confirmLabel={t('Reset')} busy={resetting} error={resetError} onCancel={()=>setPendingReset(null)} onConfirm={confirmReset}/>}
  </>
}
