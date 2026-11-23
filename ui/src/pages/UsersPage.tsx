import { useEffect, useRef, useState } from 'react'
import { Button, Input, Loading, Select } from '@geist-ui/core'
import { UserPlus } from '@geist-ui/icons'
import { api, jsonBody } from '../api'
import type { QuotaSettings, Role, User } from '../types'
import { useI18n } from '../i18n'
import { PageHeader } from '../components/PageHeader'
import { Status } from '../components/Status'
import { Empty } from '../components/Empty'
import { Pagination } from '../components/Pagination'
import { ConfirmModal } from '../components/ConfirmModal'
import { usePaged } from '../hooks/usePaged'
import { useFitRows } from '../hooks/useFitRows'
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
  // Page size follows the viewport height so the table fills the pane without a scrollbar.
  const body=useRef<HTMLTableSectionElement>(null)
  const fit=useFitRows(body,visible.length)
  const userPager=usePaged(visible,fit)
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
  const [creating,setCreating]=useState(false)
  if(!users)return list.error?<LoadError error={list.error} onRetry={list.reload}/>:<Loading/>
  return <>
    <PageHeader title={t('User management')} description={t('Local accounts, repository ownership and retrieval usage.')}
      actions={<>
        <Input crossOrigin="" scale={0.75} clearable placeholder={t('Filter by username')} aria-label={t('Filter by username')} value={query} onChange={e=>setQuery(e.target.value)}/>
        <Button auto scale={0.75} type="secondary" icon={<UserPlus/>} onClick={()=>setCreating(true)}>{t('New user')}</Button>
      </>}/>
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
        <tbody ref={body}>{userPager.paged.map(user=><tr key={user.id}>
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
    {creating&&<CreateUserModal onClose={()=>setCreating(false)} onCreated={()=>{setCreating(false);setQuery('');load()}}/>}
    {pendingReset&&<ConfirmModal title={t('Reset quota')} target={pendingReset.username} text={t("Reset today's counters for this user?")} confirmLabel={t('Reset')} busy={resetting} error={resetError} onCancel={()=>setPendingReset(null)} onConfirm={confirmReset}/>}
  </>
}

// CreateUserModal: an administrator opens an account directly. Username and
// password follow the sign-up rules (checked server-side); email is optional.
// Same .modal-* shell as ConfirmModal: Escape/backdrop close unless busy.
function CreateUserModal({onClose,onCreated}:{onClose:()=>void;onCreated:()=>void}){
  const {t}=useI18n()
  const [username,setUsername]=useState('')
  const [email,setEmail]=useState('')
  const [password,setPassword]=useState('')
  const [role,setRole]=useState<Role>('user')
  const [busy,setBusy]=useState(false)
  const [error,setError]=useState('')
  useEffect(()=>{const onKey=(e:KeyboardEvent)=>{if(e.key==='Escape'&&!busy)onClose()};document.addEventListener('keydown',onKey);return()=>document.removeEventListener('keydown',onKey)},[busy,onClose])
  const cancel=()=>{if(!busy)onClose()}
  const submit=async(e:React.FormEvent)=>{
    e.preventDefault()
    if(busy)return
    setBusy(true);setError('')
    try{await api<User>('/api/v1/admin/users',{method:'POST',...jsonBody({username:username.trim(),email:email.trim(),password,role})});onCreated()}
    catch(err){setError(t((err as Error).message));setBusy(false)}
  }
  return <div className="modal-backdrop" onClick={cancel}><form className="modal-card" role="dialog" aria-modal="true" aria-label={t('New user')} onClick={e=>e.stopPropagation()} onSubmit={submit}>
    <h3>{t('New user')}</h3>
    <p className="modal-text">{t('The account can sign in right away with this username and password.')}</p>
    <div className="modal-form">
      <Input crossOrigin="" width="100%" autoFocus autoComplete="off" value={username} onChange={e=>setUsername(e.target.value)}>{t('Username')}</Input>
      <Input crossOrigin="" width="100%" autoComplete="off" placeholder={t('Optional')} value={email} onChange={e=>setEmail(e.target.value)}>{t('Email')}</Input>
      <div>
        <Input.Password crossOrigin="" width="100%" autoComplete="new-password" value={password} onChange={e=>setPassword(e.target.value)}>{t('Password')}</Input.Password>
        <p className="modal-hint">{t('At least 10 characters.')}</p>
      </div>
      <div className="modal-field">
        <span>{t('Role')}</span>
        <Select width="100%" value={role} onChange={v=>setRole(v as Role)} aria-label={t('Role')}>
          <Select.Option value="user">{t('user')}</Select.Option>
          <Select.Option value="admin">{t('admin')}</Select.Option>
        </Select>
      </div>
    </div>
    {error&&<p className="form-error">{error}</p>}
    <div className="modal-actions"><Button auto scale={.8} onClick={cancel} disabled={busy}>{t('Cancel')}</Button><Button auto scale={.8} type="secondary" htmlType="submit" loading={busy} disabled={!username.trim()||!password}>{t('Create')}</Button></div>
  </form></div>
}
