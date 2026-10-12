import { useEffect, useState } from 'react'
import { Button, Loading } from '@geist-ui/core'
import { Check, Copy } from '@geist-ui/icons'
import { api, jsonBody } from '../api'
import type { AceProject, QuotaStatus, User } from '../types'
import { useI18n } from '../i18n'
import { PageHeader } from '../components/PageHeader'
import { Panel } from '../components/Panel'
import { ConfirmModal } from '../components/ConfirmModal'
import { QuotaBar } from '../components/QuotaBar'
import { useCopy } from '../hooks/useCopy'
import { bytes, formatDate, maskToken, projectLabel } from '../lib/format'

export function AccountPage({user}:{user:User}){
  const {t,language}=useI18n()
  const [aceToken,setAceToken]=useState('')
  const [busy,setBusy]=useState(false)
  const [copied,copy]=useCopy()
  const [quota,setQuota]=useState<QuotaStatus|null>(null)
  useEffect(()=>{
    api<{token:string}>('/api/v1/me/ace-token').then(v=>setAceToken(v.token)).catch(()=>{})
    api<QuotaStatus>('/api/v1/me/quota').then(setQuota).catch(()=>{})
  },[])
  const generate=async()=>{setBusy(true);try{const v=await api<{token:string}>('/api/v1/me/ace-token',{method:'POST'});setAceToken(v.token)}finally{setBusy(false)}}
  return <>
    <PageHeader title={t('Account settings')} description={t('Local identity and session details.')}/>
    <div className="account-columns">
      <Panel title={t('Profile')}>
        <div className="account-card">
          <span className="avatar large">{user.avatar_url?<img src={user.avatar_url} alt=""/>:user.username.slice(0,2).toUpperCase()}</span>
          <div>
            <h2>{user.linuxdo_username||user.username}{user.linuxdo_id!=null&&user.trust_level!=null&&<span className="trust-badge">Lv{user.trust_level}</span>}</h2>
            <p>{t('Role')}: <code className="inline-code">{t(user.role)}</code></p>
            <small>{t('Created')} {formatDate(user.created_at,language)} · {t('Last active prefix')} {formatDate(user.last_active_at,language)}</small>
          </div>
        </div>
      </Panel>
      <RetrievalPrefPanel user={user}/>
    </div>
    <div className="account-columns">
      <Panel title={t('BCE token')}>
        <p className="settings-intro">{t('Personal token for MCP / BCE clients. Generating a new token invalidates the previous one.')}</p>
        {aceToken
          ?<div className="token-row">
            <code className="inline-code">{maskToken(aceToken)}</code>
            <button type="button" className="icon-button" onClick={()=>copy(aceToken)} aria-label={t('Copy token')} title={t(copied?'Copied':'Copy token')}>{copied?<Check size={14}/>:<Copy size={14}/>}</button>
            {copied&&<small className="token-copied">{t('Copied')}</small>}
          </div>
          :<p className="settings-intro">{t('No token generated yet.')}</p>}
        <div className="settings-actions"><Button auto loading={busy} onClick={generate}>{t(aceToken?'Regenerate BCE token':'Generate BCE token')}</Button></div>
      </Panel>
      <Panel title={t('Usage & limits')}>
        {!quota?<Loading/>
        :quota.unlimited?<p className="settings-intro">{t('Administrator accounts are not subject to quotas.')}</p>
        :<>
          <div className="quota-list">
            <QuotaBar label={t('Retrieval today')} used={quota.usage.retrieval} limit={quota.limits.daily_retrieval} tone="blue"/>
            <QuotaBar label={t('Enhance today')} used={quota.usage.enhance} limit={quota.limits.daily_enhance} tone="warn"/>
            <QuotaBar label={t('Uploads today')} used={quota.usage.upload} limit={quota.limits.daily_upload} tone="ok"/>
            <QuotaBar label={t('Storage')} used={quota.usage.storage_bytes} limit={quota.limits.storage_limit_mb*1048576} tone="neutral" format={bytes}/>
          </div>
          <p className="quota-note">{t('Daily counters reset at midnight. Storage is a standing limit.')}</p>
        </>}
        <AutoPurgeSection user={user}/>
      </Panel>
    </div>
  </>
}

// RetrievalPrefPanel: per-user cap on how many tokens one MCP retrieval may
// return (PATCH /api/v1/me/settings); the server clamps every retrieval to it.
function RetrievalPrefPanel({user}:{user:User}){
  const {t}=useI18n()
  const [value,setValue]=useState(user.max_output_tokens||6400)
  const [busy,setBusy]=useState(false)
  const [saved,setSaved]=useState(false)
  const [error,setError]=useState('')
  // The user prop comes from the auth state App loaded once at sign-in; a
  // save here never updates that copy, so a remount (every menu switch)
  // would re-initialize from the stale value — save 8000, switch away and
  // back, see the old number again. Refetch the authoritative value on
  // mount; the prop only seeds the input until this lands.
  useEffect(()=>{
    api<{user:User}>('/api/v1/me').then(v=>{if(v.user.max_output_tokens)setValue(v.user.max_output_tokens)}).catch(()=>{})
  },[])
  const save=async()=>{
    setBusy(true);setSaved(false);setError('')
    try{
      await api('/api/v1/me/settings',{method:'PATCH',...jsonBody({max_output_tokens:value})})
      setSaved(true)
    }catch(e){setError((e as Error).message)}
    finally{setBusy(false)}
  }
  return <Panel title={t('Retrieval preferences')}>
    <p className="settings-intro">{t('Cap on how many tokens a single retrieval may return to your MCP client.')}</p>
    <div className="pref-row">
      <span className="pref-label">{t('Max output per retrieval (tokens)')}</span>
      <div className="pref-controls">
        {saved&&<span className="pref-saved">{t('Configuration saved')}</span>}
        <input type="number" min={1600} max={24000} step={800} value={value} onChange={e=>{setValue(Number(e.target.value)||0);setSaved(false)}}/>
        <Button auto scale={.6} loading={busy} disabled={value<1600||value>24000} onClick={save}>{t('Save')}</Button>
      </div>
    </div>
    <ul className="pref-notes">
      <li>{t('Allowed range: 1600 – 24000. Default: 6400.')}</li>
      <li>{t('Lower: saves agent context; complex questions may return fewer fragments.')}</li>
      <li>{t('Higher: more complete retrievals; fills the context window faster.')}</li>
    </ul>
    {error&&<p className="form-error">{error}</p>}
  </Panel>
}

const PURGE_DAYS=[14,30,60,90]
type PurgePreview={projects:AceProject[];storage_bytes:number}

// AutoPurgeSection: opt-in background cleanup of projects with no retrieval
// for N days (PATCH /api/v1/me/settings auto_purge_days, 0 = off; the server
// sweeps hourly). It sits under the quota bars, so it stays one row tall and
// saves on change: switching on or picking a window that already catches
// projects first previews them and asks for confirmation.
function AutoPurgeSection({user}:{user:User}){
  const {t}=useI18n()
  const [days,setDays]=useState(user.auto_purge_days||0)
  const [pending,setPending]=useState<{days:number;preview:PurgePreview}|null>(null)
  const [busy,setBusy]=useState(false)
  const [error,setError]=useState('')
  // Same stale-prop problem as RetrievalPrefPanel: refetch the saved value.
  useEffect(()=>{
    api<{user:User}>('/api/v1/me').then(v=>setDays(v.user.auto_purge_days||0)).catch(()=>{})
  },[])
  const persist=async(next:number)=>{
    setBusy(true);setError('')
    try{
      await api('/api/v1/me/settings',{method:'PATCH',...jsonBody({auto_purge_days:next})})
      setDays(next);setPending(null)
    }catch(e){setError((e as Error).message)}
    finally{setBusy(false)}
  }
  const choose=async(next:number)=>{
    if(busy||next===days)return
    if(!next){void persist(0);return}
    setBusy(true);setError('')
    try{
      const preview=await api<PurgePreview>(`/api/v1/me/auto-purge/preview?days=${next}`)
      setBusy(false)
      if(preview.projects.length)setPending({days:next,preview})
      else void persist(next)
    }catch(e){setError((e as Error).message);setBusy(false)}
  }
  const names=(pending?.preview.projects||[]).map(p=>projectLabel(p.name,p.branch))
  return <div className="auto-purge">
    <div className="pref-row">
      <span className="pref-label">{t('Auto-clean idle projects')}</span>
      <div className="pref-controls">
        {/* Always visible so the windows are discoverable; while off they are
            dimmed and picking one switches cleanup on with that window. */}
        <div className={`purge-days${days?'':' off'}`} role="radiogroup" aria-label={t('Idle for')}>
          {PURGE_DAYS.map(d=><button key={d} type="button" role="radio" aria-checked={days===d} className={days===d?'active':''} disabled={busy} onClick={()=>void choose(d)}>{t('{n} days',{n:d})}</button>)}
        </div>
        <label className="toggle"><input type="checkbox" checked={days>0} disabled={busy} aria-label={t('Auto-clean idle projects')} onChange={e=>void choose(e.target.checked?30:0)}/><span/></label>
      </div>
    </div>
    <p className="quota-note">{t('Idle = no retrieval in the window (searching alone counts as active); a cleaned project re-indexes on its next search.')}</p>
    {error&&!pending&&<p className="form-error">{error}</p>}
    {pending&&<ConfirmModal
      title={t('Enable auto-clean')}
      target={names.length>5?t('{list} and {n} more',{list:names.slice(0,5).join(', '),n:names.length-5}):names.join(', ')}
      text={t('These projects (up to {size}) have had no retrieval for {n} days and will be permanently cleaned within the hour; each is re-indexed on its next search.',{n:pending.days,size:bytes(pending.preview.storage_bytes)})}
      confirmLabel={t('Enable')} danger busy={busy} error={error}
      onCancel={()=>{if(!busy){setPending(null);setError('')}}} onConfirm={()=>void persist(pending.days)}/>}
  </div>
}
