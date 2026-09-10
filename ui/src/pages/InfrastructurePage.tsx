import { useEffect, useState } from 'react'
import { Button, Loading } from '@geist-ui/core'
import { api, jsonBody } from '../api'
import type { RetrievalSettings, SystemSettings } from '../types'
import { useI18n } from '../i18n'
import { PageHeader } from '../components/PageHeader'
import { Panel } from '../components/Panel'
import { Setting } from '../components/Setting'

export function InfrastructurePage(){
  const {t}=useI18n()
  const [settings,setSettings]=useState<RetrievalSettings|null>(null)
  const [busy,setBusy]=useState(false)
  const [saved,setSaved]=useState(false)
  const [error,setError]=useState('')
  const load=()=>api<RetrievalSettings>('/api/v1/admin/settings/retrieval').then(setSettings)
  useEffect(()=>{void load()},[])
  const update=<K extends keyof RetrievalSettings>(key:K,value:RetrievalSettings[K])=>setSettings(current=>current?{...current,[key]:value}:current)
  const save=async()=>{
    if(!settings)return
    setBusy(true);setSaved(false);setError('')
    try{
      setSettings(await api<RetrievalSettings>('/api/v1/admin/settings/retrieval',{method:'PATCH',...jsonBody(settings)}))
      setSaved(true)
    }catch(e){setError((e as Error).message)}
    finally{setBusy(false)}
  }
  if(!settings)return <Loading/>
  return <>
    <PageHeader title={t('System settings')} description={t('Model endpoints for retrieval plus system-level switches.')}/>
    <RegistrationPanel/>
    <Panel title={t('Retrieval configuration')} meta="ADMIN ONLY">
      <p className="settings-intro">{t('Only administrators can change these values. Changes are persisted and audited.')}</p>
      {/* Two symmetric provider blocks, same field order: provider → URL →
          keys → models. Each block owns its credentials; the old shared-key
          section is gone (the env-level fallback still exists server-side). */}
      {/* One card per provider connection, both with the same anatomy:
          head (path eyebrow + role) → connection cluster → models cluster.
          The rerank feature nests inside the semantic card with its toggle
          as the block's master switch. */}
      <div className="model-cards">
        <section className="model-card model-card-semantic">
          <header className="model-card-head">
            <span className="mc-eyebrow">SEMANTIC · EMBEDDING + RERANK</span>
            <h3>{t('Embedding & reranker')}</h3>
            <p>{t('Both models come from the same provider: one endpoint and key, two model names.')}</p>
          </header>
          <div className="mc-cluster">
            <span className="mc-cluster-label">{t('Connection')}</span>
            <div className="mc-row mc-row-provider">
              <Setting label={t('Provider')}><select value={settings.embedding_provider} onChange={e=>update('embedding_provider',e.target.value)}><option value="openai-compatible">OpenAI-compatible</option><option value="ollama">Ollama</option></select></Setting>
              <Setting label={t('Service URL')}><input value={settings.embedding_url} onChange={e=>update('embedding_url',e.target.value)} placeholder="https://api.voyageai.com/v1"/></Setting>
            </div>
            <Setting label={t('API keys')}>
              <textarea className="api-keys-input" rows={2} autoComplete="off" spellCheck={false} value={settings.embedding_api_key} onChange={e=>update('embedding_api_key',e.target.value)}
                placeholder={t('One per line; multiple keys are load-balanced')}/>
            </Setting>
          </div>
          <div className="mc-cluster">
            <span className="mc-cluster-label">{t('Models')}</span>
            <div className="mc-row mc-row-model">
              <Setting label={t('Embedding model')}><input value={settings.embedding_model} onChange={e=>update('embedding_model',e.target.value)} placeholder="voyage-code-4"/></Setting>
              <Setting label={t('Dimensions')}><input type="number" min="128" max="8192" value={settings.embedding_dimensions} onChange={e=>update('embedding_dimensions',Number(e.target.value)||0)}/></Setting>
            </div>
            <div className="mc-feature">
              <div className="mc-feature-head">
                <div>
                  <strong>{t('Reranker')}</strong>
                  <small>{t('Cross-encoder pass over the top candidates.')}</small>
                </div>
                <label className="toggle"><input type="checkbox" checked={settings.reranker_enabled} onChange={e=>update('reranker_enabled',e.target.checked)}/><span/></label>
              </div>
              {settings.reranker_enabled&&<div className="mc-row mc-row-model">
                <Setting label={t('Reranker model')}><input value={settings.reranker_model} onChange={e=>update('reranker_model',e.target.value)} placeholder="rerank-2.5"/></Setting>
                <Setting label={t('Reranker Top-K')}><input type="number" min="1" max="200" value={settings.reranker_top_k} onChange={e=>update('reranker_top_k',Number(e.target.value)||0)}/></Setting>
              </div>}
            </div>
          </div>
        </section>
        <section className="model-card model-card-language">
          <header className="model-card-head">
            <span className="mc-eyebrow">LANGUAGE · ENHANCE + SUMMARY</span>
            <h3>{t('Enhancer & summaries')}</h3>
            <p>{t('Chat models from one provider: prompt enhancement, broad-query decomposition and chunk summaries.')}</p>
          </header>
          <div className="mc-cluster">
            <span className="mc-cluster-label">{t('Connection')}</span>
            <div className="mc-row mc-row-provider">
              <Setting label={t('Provider')}><select value={settings.enhancer_provider} onChange={e=>update('enhancer_provider',e.target.value)}><option value="openai-compatible">OpenAI-compatible</option><option value="ollama">Ollama</option></select></Setting>
              <Setting label={t('Service URL')}><input value={settings.enhancer_url} onChange={e=>update('enhancer_url',e.target.value)} placeholder="https://api.siliconflow.cn/v1"/></Setting>
            </div>
            <Setting label={t('API keys')}>
              <textarea className="api-keys-input" rows={2} autoComplete="off" spellCheck={false} value={settings.enhancer_api_key} onChange={e=>update('enhancer_api_key',e.target.value)}
                placeholder={t('One per line; multiple keys are load-balanced')}/>
            </Setting>
          </div>
          <div className="mc-cluster">
            <span className="mc-cluster-label">{t('Models')}</span>
            <Setting label={t('Enhancer model')}><input value={settings.enhancer_model} onChange={e=>update('enhancer_model',e.target.value)} placeholder="Qwen/Qwen3.5-9B"/></Setting>
            <div className="mc-row mc-row-model">
              <Setting label={t('Summary model')}><input value={settings.summary_model} onChange={e=>update('summary_model',e.target.value)} placeholder="Qwen/Qwen3-8B"/></Setting>
              <Setting label={t('Summary budget')}><input type="number" min="0" max="500" value={settings.summary_budget} onChange={e=>update('summary_budget',Number(e.target.value)||0)}/></Setting>
            </div>
          </div>
        </section>
      </div>
      {error&&<p className="form-error">{error}</p>}
      <div className="settings-actions">{saved&&<span>{t('Configuration saved')}</span>}<Button auto type="secondary" loading={busy} onClick={save}>{t('Save configuration')}</Button></div>
    </Panel>
  </>
}
// RegistrationPanel is the system-level registration switch: it persists to
// system_settings immediately on toggle and gates both email signup and
// first-time LinuxDo account creation.
function RegistrationPanel(){
  const {t}=useI18n()
  const [settings,setSettings]=useState<SystemSettings|null>(null)
  const [saved,setSaved]=useState(false)
  const [error,setError]=useState('')
  useEffect(()=>{api<SystemSettings>('/api/v1/admin/settings/system').then(setSettings).catch(e=>setError((e as Error).message))},[])
  const toggle=async(next:boolean)=>{
    if(!settings)return
    const previous=settings
    setSettings({...settings,registration_enabled:next});setSaved(false);setError('')
    try{setSettings(await api<SystemSettings>('/api/v1/admin/settings/system',{method:'PATCH',...jsonBody({registration_enabled:next})}));setSaved(true)}
    catch(e){setError((e as Error).message);setSettings(previous)}
  }
  return <Panel title={t('Registration')} meta="ADMIN ONLY">
    <p className="settings-intro">{t('Gates email signup and first-time LinuxDo account creation; existing accounts keep signing in.')}</p>
    {settings?<div className="settings-grid">
      <Setting label={t('Allow self-service signup')}><label className="toggle"><input type="checkbox" checked={settings.registration_enabled} onChange={e=>toggle(e.target.checked)}/><span/></label></Setting>
    </div>:!error&&<Loading/>}
    {settings&&settings.registration_enabled&&!settings.smtp_configured&&<p className="form-error">{t('SMTP is not configured — the email signup entry stays hidden until it is.')}</p>}
    {error&&<p className="form-error">{error}</p>}
    {saved&&<div className="settings-actions"><span>{t('Configuration saved')}</span></div>}
  </Panel>
}
