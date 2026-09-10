import { useEffect, useState } from 'react'
import { Button, Loading } from '@geist-ui/core'
import { api, jsonBody } from '../api'
import type { QuotaSettings } from '../types'
import { useI18n } from '../i18n'
import { PageHeader } from '../components/PageHeader'
import { Panel } from '../components/Panel'
import { Setting } from '../components/Setting'

export function QuotaPage(){
  const {t}=useI18n()
  const [settings,setSettings]=useState<QuotaSettings|null>(null)
  const [busy,setBusy]=useState(false)
  const [saved,setSaved]=useState(false)
  const [error,setError]=useState('')
  useEffect(()=>{api<QuotaSettings>('/api/v1/admin/settings/quota').then(setSettings)},[])
  const update=<K extends keyof QuotaSettings>(key:K,value:QuotaSettings[K])=>setSettings(current=>current?{...current,[key]:value}:current)
  const save=async()=>{
    if(!settings)return
    setBusy(true);setSaved(false);setError('')
    try{
      setSettings(await api<QuotaSettings>('/api/v1/admin/settings/quota',{method:'PATCH',...jsonBody(settings)}))
      setSaved(true)
    }catch(e){setError((e as Error).message)}
    finally{setBusy(false)}
  }
  if(!settings)return <Loading/>
  return <>
    <PageHeader title={t('Quota & rate limits')} description={t('Default quotas for regular users and per-minute rate limits for BCE endpoints.')}/>
    <Panel title={t('Quota & rate limits')} meta="ADMIN ONLY">
      <p className="settings-intro">{t('Only administrators can change these values. Changes are persisted and audited.')}</p>
      <div className="settings-section">
        <h3 className="settings-section-title">{t('Default quotas')}</h3>
        <p className="settings-section-hint">{t('Applied to every non-admin user immediately.')}</p>
        <div className="settings-grid">
          <Setting label={t('Storage limit (MB)')}><input type="number" min="1" value={settings.storage_limit_mb} onChange={e=>update('storage_limit_mb',Number(e.target.value)||0)}/></Setting>
          <Setting label={t('Daily retrieval')}><input type="number" min="1" value={settings.daily_retrieval} onChange={e=>update('daily_retrieval',Number(e.target.value)||0)}/></Setting>
          <Setting label={t('Daily enhance')}><input type="number" min="1" value={settings.daily_enhance} onChange={e=>update('daily_enhance',Number(e.target.value)||0)}/></Setting>
          <Setting label={t('Daily upload')}><input type="number" min="1" value={settings.daily_upload} onChange={e=>update('daily_upload',Number(e.target.value)||0)}/></Setting>
        </div>
      </div>
      <div className="settings-section">
        <h3 className="settings-section-title">{t('Rate limits (per minute)')}</h3>
        <p className="settings-section-hint">{t('Requests beyond the limit receive an immediate rate-limit error.')}</p>
        <div className="settings-grid">
          <Setting label={t('Retrieval RPM')}><input type="number" min="1" value={settings.rpm_retrieval} onChange={e=>update('rpm_retrieval',Number(e.target.value)||0)}/></Setting>
          <Setting label={t('Upload RPM')}><input type="number" min="1" value={settings.rpm_upload} onChange={e=>update('rpm_upload',Number(e.target.value)||0)}/></Setting>
          <Setting label={t('Enhance RPM')}><input type="number" min="1" value={settings.rpm_enhance} onChange={e=>update('rpm_enhance',Number(e.target.value)||0)}/></Setting>
        </div>
      </div>
      {error&&<p className="form-error">{error}</p>}
      <div className="settings-actions">{saved&&<span>{t('Configuration saved')}</span>}<Button auto type="secondary" loading={busy} onClick={save}>{t('Save quotas')}</Button></div>
    </Panel>
  </>
}
