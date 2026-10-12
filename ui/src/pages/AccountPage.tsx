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
      <input type="number" min={1600} max={24000} step={800} value={value} onChange={e=>setValue(Number(e.target.value)||0)}/>
    </div>
    <ul className="pref-notes">
      <li>{t('Allowed range: 1600 – 24000. Default: 6400.')}</li>
      <li>{t('Lower: saves agent context; complex questions may return fewer fragments.')}</li>
      <li>{t('Higher: more complete retrievals; fills the context window faster.')}</li>
    </ul>
    {error&&<p className="form-error">{error}</p>}
    <div className="settings-actions">{saved&&<span>{t('Configuration saved')}</span>}<Button auto loading={busy} disabled={value<1600||value>24000} onClick={save}>{t('Save')}</Button></div>
  </Panel>
}

const PURGE_DAYS=[14,30,60,90]

// AutoPurgeSection: opt-in background cleanup of projects with no retrieval
// for N days (PATCH /api/v1/me/settings auto_purge_days, 0 = off). The server
// sweeps hourly; the preview shows what the chosen window would purge now,
// and saving a window that already catches projects asks for confirmation.
function AutoPurgeSection({user}:{user:User}){
  const {t}=useI18n()
  const [saved,setSaved]=useState(user.auto_purge_days||0)
  const [enabled,setEnabled]=useState(saved>0)
  const [days,setDays]=useState(saved||30)
  const [preview,setPreview]=useState<{projects:AceProject[];storage_bytes:number}|null>(null)
  const [busy,setBusy]=useState(false)
  const [done,setDone]=useState(false)
  const [error,setError]=useState('')
  const [confirming,setConfirming]=useState(false)
  // Same stale-prop problem as RetrievalPrefPanel: refetch the saved value.
  useEffect(()=>{
    api<{user:User}>('/api/v1/me').then(v=>{const d=v.user.auto_purge_days||0;setSaved(d);setEnabled(d>0);if(d)setDays(d)}).catch(()=>{})
  },[])
  useEffect(()=>{
    if(!enabled){setPreview(null);return}
    api<{projects:AceProject[];storage_bytes:number}>(`/api/v1/me/auto-purge/preview?days=${days}`).then(setPreview).catch(()=>setPreview(null))
  },[enabled,days])
  const target=enabled?days:0
  const persist=async()=>{
    setBusy(true);setDone(false);setError('')
    try{
      await api('/api/v1/me/settings',{method:'PATCH',...jsonBody({auto_purge_days:target})})
      setSaved(target);setDone(true);setConfirming(false)
    }catch(e){setError((e as Error).message)}
    finally{setBusy(false)}
  }
  const save=()=>{if(target>0&&preview&&preview.projects.length>0)setConfirming(true);else void persist()}
  const names=(preview?.projects||[]).map(p=>projectLabel(p.name,p.branch))
  return <div className="auto-purge">
    <div className="pref-row">
      <span className="pref-label">{t('Auto-clean idle projects')}</span>
      <label className="toggle"><input type="checkbox" checked={enabled} aria-label={t('Auto-clean idle projects')} onChange={e=>{setEnabled(e.target.checked);setDone(false)}}/><span/></label>
    </div>
    {enabled&&<div className="purge-days" role="radiogroup" aria-label={t('Idle for')}>
      <span>{t('Idle for')}</span>
      {PURGE_DAYS.map(d=><button key={d} type="button" role="radio" aria-checked={days===d} className={days===d?'active':''} onClick={()=>{setDays(d);setDone(false)}}>{t('{n} days',{n:d})}</button>)}
    </div>}
    <ul className="pref-notes">
      <li>{t('Activity is the last retrieval: a project you only search, without editing, stays active.')}</li>
      <li>{t('A cleaned project is re-uploaded and re-indexed automatically on its next search.')}</li>
      {enabled&&preview&&<li>{preview.projects.length
        ?t('With this window, {n} projects (up to {size}) would be cleaned now.',{n:preview.projects.length,size:bytes(preview.storage_bytes)})
        :t('No project is idle that long right now.')}</li>}
    </ul>
    {error&&!confirming&&<p className="form-error">{error}</p>}
    <div className="settings-actions">{done&&<span>{t('Configuration saved')}</span>}<Button auto loading={busy&&!confirming} disabled={target===saved} onClick={save}>{t('Save')}</Button></div>
    {confirming&&<ConfirmModal
      title={t('Enable auto-clean')}
      target={names.length>5?t('{list} and {n} more',{list:names.slice(0,5).join(', '),n:names.length-5}):names.join(', ')}
      text={t('These projects have had no retrieval for {n} days and will be permanently cleaned within the hour; each is re-indexed on its next search.',{n:days})}
      confirmLabel={t('Enable')} danger busy={busy} error={error}
      onCancel={()=>{if(!busy){setConfirming(false);setError('')}}} onConfirm={()=>void persist()}/>}
  </div>
}
