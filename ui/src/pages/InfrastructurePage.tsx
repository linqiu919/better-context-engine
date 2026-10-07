import { useEffect, useState } from 'react'
import { Button, Loading } from '@geist-ui/core'
import { CheckInCircle, Eye, EyeOff, X, XCircle } from '@geist-ui/icons'
import { api, jsonBody } from '../api'
import type { ModelTestResult, RetrievalSettings, SystemSettings } from '../types'
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
            <ApiKeysField value={settings.embedding_api_key} onChange={v=>update('embedding_api_key',v)}/>
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
          <ModelTest target="semantic" settings={settings}/>
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
            <ApiKeysField value={settings.enhancer_api_key} onChange={v=>update('enhancer_api_key',v)}/>
            {/* OpenRouter fronts many upstream providers for one model; the
                listed slugs are tried first, in order, before its own routing. */}
            {settings.enhancer_url.includes('openrouter')&&<ProviderTagsField value={settings.enhancer_providers??[]} onChange={v=>update('enhancer_providers',v)}/>}
          </div>
          <div className="mc-cluster">
            <span className="mc-cluster-label">{t('Models')}</span>
            <Setting label={t('Enhancer model')}><input value={settings.enhancer_model} onChange={e=>update('enhancer_model',e.target.value)} placeholder="Qwen/Qwen3.5-9B"/></Setting>
            <div className="mc-row mc-row-model">
              <Setting label={t('Summary model')}><input value={settings.summary_model} onChange={e=>update('summary_model',e.target.value)} placeholder="Qwen/Qwen3-8B"/></Setting>
              <Setting label={t('Summary budget')}><input type="number" min="0" max="500" value={settings.summary_budget} onChange={e=>update('summary_budget',Number(e.target.value)||0)}/></Setting>
            </div>
          </div>
          <ModelTest target="language" settings={settings}/>
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

// maskKey keeps the first and last 4 characters of each key so admins can
// tell keys apart without exposing them; short values are fully hidden.
const maskKey=(k:string)=>k.length<=12?'•'.repeat(k.length):`${k.slice(0,4)}${'•'.repeat(8)}${k.slice(-4)}`

// ApiKeysField shows stored keys masked (one per line) until the eye toggle
// reveals the editable plaintext. Not built on <Setting>: its <label> would
// retarget label clicks onto the toggle button.
function ApiKeysField({value,onChange}:{value:string;onChange:(v:string)=>void}){
  const {t}=useI18n()
  const [shown,setShown]=useState(false)
  const masked=!shown&&value.trim()!==''
  const display=masked?value.split(/\r?\n/).map(line=>line.trim()&&maskKey(line.trim())).join('\n'):value
  return <div className="setting-field">
    <span className="setting-field-head">{t('API keys')}
      {value.trim()!==''&&<button type="button" className="icon-toggle" onClick={()=>setShown(v=>!v)} title={shown?t('Hide keys'):t('Show keys')} aria-label={shown?t('Hide keys'):t('Show keys')}>{shown?<EyeOff size={13}/>:<Eye size={13}/>}</button>}
    </span>
    <textarea className="api-keys-input" rows={2} autoComplete="off" spellCheck={false} value={display} readOnly={masked}
      onChange={e=>onChange(e.target.value)} onFocus={()=>{if(masked)setShown(true)}}
      placeholder={t('One per line; multiple keys are load-balanced')}/>
  </div>
}

// ProviderTagsField edits the ordered OpenRouter provider slug list: Enter or
// comma commits a tag, Backspace on an empty input removes the last one.
function ProviderTagsField({value,onChange}:{value:string[];onChange:(v:string[])=>void}){
  const {t}=useI18n()
  const [draft,setDraft]=useState('')
  const commit=()=>{
    const added=draft.split(/[\s,]+/).map(v=>v.trim()).filter(v=>v&&!value.includes(v))
    if(added.length)onChange([...value,...added])
    setDraft('')
  }
  return <div className="setting-field">
    <span>{t('OpenRouter providers')}</span>
    <div className="tag-input">
      {value.map((p,i)=><span key={p} className="tag-chip"><em>{i+1}</em>{p}<button type="button" onClick={()=>onChange(value.filter(v=>v!==p))} aria-label={t('Remove')}><X size={11}/></button></span>)}
      <input value={draft} onChange={e=>setDraft(e.target.value)} onBlur={commit}
        onKeyDown={e=>{
          if(e.key==='Enter'||e.key===','){e.preventDefault();commit()}
          else if(e.key==='Backspace'&&!draft&&value.length)onChange(value.slice(0,-1))
        }}
        placeholder={value.length?'':'deepinfra, together, …'}/>
    </div>
    <small className="setting-hint">{t('Provider slugs tried first, in order; OpenRouter falls back to its own routing if all are unavailable. Leave empty for default routing.')}</small>
  </div>
}

// ModelTest probes one card's current (possibly unsaved) form values against
// the live provider. Results are dropped whenever the form changes so a stale
// pass never vouches for edited values.
function ModelTest({target,settings}:{target:'semantic'|'language';settings:RetrievalSettings}){
  const {t}=useI18n()
  const [busy,setBusy]=useState(false)
  const [result,setResult]=useState<ModelTestResult|null>(null)
  const [error,setError]=useState('')
  useEffect(()=>{setResult(null);setError('')},[settings])
  const run=async()=>{
    setBusy(true);setResult(null);setError('')
    try{setResult(await api<ModelTestResult>('/api/v1/admin/settings/retrieval/test',{method:'POST',...jsonBody({target,settings})}))}
    catch(e){setError((e as Error).message)}
    finally{setBusy(false)}
  }
  const label:Record<string,string>={embedding:t('Embedding model'),reranker:t('Reranker model'),enhancer:t('Enhancer model'),summary:t('Summary model')}
  return <div className="mc-test">
    <div className="mc-test-head">
      <small>{t('Tests the values in this card before saving.')}</small>
      <Button auto scale={0.7} loading={busy} onClick={run}>{t('Test connection')}</Button>
    </div>
    {error&&<p className="form-error">{error}</p>}
    {result&&<ul className="mc-test-results">
      {result.checks.map(c=><li key={c.name} className={c.ok?'ok':'bad'}>
        {c.ok?<CheckInCircle size={14}/>:<XCircle size={14}/>}
        <div>
          <strong>{label[c.name]??c.name}</strong><code>{c.model||'—'}</code>{c.ms>0&&<span className="mc-test-ms">{c.ms} ms</span>}
          <p>{c.detail}</p>
        </div>
      </li>)}
    </ul>}
  </div>
}
