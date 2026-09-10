import { useEffect, useState } from 'react'
import { Button, Loading, Select } from '@geist-ui/core'
import { Play } from '@geist-ui/icons'
import { api, jsonBody } from '../api'
import type { AceProject, SearchResponse } from '../types'
import { useI18n } from '../i18n'
import { PageHeader } from '../components/PageHeader'
import { Empty } from '../components/Empty'
import { Score } from '../components/Score'
import { compact, ms } from '../lib/format'

// SearchPage runs a retrieval against one of the user's ready projects and
// shows why each fragment was selected (ACE response + ranked hits).
export function SearchPage(){
  const {t}=useI18n()
  const [projects,setProjects]=useState<AceProject[]>([]),[project,setProject]=useState(''),[query,setQuery]=useState('')
  const [result,setResult]=useState<SearchResponse|null>(null),[busy,setBusy]=useState(false),[error,setError]=useState('')
  const isReady=(p:AceProject)=>!!p.snapshot_id&&p.chunk_count>0&&p.embedded_count>=p.chunk_count
  useEffect(()=>{
    api<{projects:AceProject[]}>('/api/v1/me/ace-projects').then(v=>{
      const withSnapshot=v.projects.filter(p=>p.snapshot_id)
      setProjects(withSnapshot)
      const first=withSnapshot.find(isReady)
      if(first)setProject(first.snapshot_id)
    }).catch(()=>{})
  },[])
  const run=async()=>{
    setBusy(true);setError('')
    try{setResult(await api('/api/v1/search/inspect',{method:'POST',...jsonBody({snapshot_id:project,query,max_output_length:0})}))}
    catch(e){setError((e as Error).message)}
    finally{setBusy(false)}
  }
  return <>
    <PageHeader title={t('Search inspect')} description={t('See exactly why a context fragment was selected.')}/>
    <div className="search-workbench">
      <section className="query-panel">
        <label>{t('Project')}</label>
        <Select width="100%" value={project} onChange={v=>setProject(String(v))}>
          {projects.map(p=><Select.Option key={(p.owner_id||'')+p.name} value={p.snapshot_id} disabled={!isReady(p)}>{isReady(p)?p.name:`${p.name} · ${t('Indexing')}`}</Select.Option>)}
        </Select>
        <label>{t('Information request')}</label>
        <textarea value={query} onChange={e=>setQuery(e.target.value)} rows={7} placeholder={t('e.g. Where is authentication implemented?')}/>
        <div className="query-hints"><span>{t('Natural language')}</span><span>{t('Account token budget')}</span><span>{t('Workspace overlay')}</span></div>
        <div className="query-actions"><Button auto type="secondary" icon={<Play/>} loading={busy} disabled={!project||!query.trim()} onClick={run}>{t('Run retrieval')}</Button></div>
        {error&&<p className="form-error">{error}</p>}
      </section>
      <section className="result-panel">
        {busy?<div className="center"><Loading>{t('Running hybrid retrieval')}</Loading></div>
        :result?<SearchResults result={result}/>
        :<Empty title={t('No retrieval yet')} text={t('Choose a project and run a query to inspect ranked context.')}/>}
      </section>
    </div>
  </>
}

function SearchResults({result}:{result:SearchResponse}){
  const {t}=useI18n()
  const [tab,setTab]=useState<'ranked'|'formatted'>('formatted')
  return <>
    <div className="result-summary">
      <div><span>{t('Latency')}</span><strong>{ms(result.duration_ms)}</strong></div>
      <div><span>{t('Candidates')}</span><strong>{result.candidate_count}</strong></div>
      <div><span>{t('Selected')}</span><strong>{result.hits?.length||0}</strong></div>
      <div><span>{t('Est. tokens')}</span><strong>{compact(result.token_estimate)}</strong></div>
      {result.degraded&&<span className="status-pill warn"><i className="status-dot warn"/><em>{t('DETERMINISTIC FALLBACK')}</em></span>}
    </div>
    <div className="tab-switch">
      <button className={tab==='formatted'?'active':''} onClick={()=>setTab('formatted')}>{t('BCE response')}</button>
      <button className={tab==='ranked'?'active':''} onClick={()=>setTab('ranked')}>{t('Ranked fragments')}</button>
    </div>
    {tab==='ranked'
      ?<div className="hit-list">{result.hits?.map((hit,i)=><article className="search-hit" key={`${hit.path}-${i}`}>
        <header>
          <span className="rank">{String(i+1).padStart(2,'0')}</span>
          <div className="hit-path"><strong title={hit.path}>{hit.path}</strong><small>{t('Lines')} {hit.start_line}–{hit.end_line}</small></div>
          <Score value={hit.score}/>
        </header>
        <pre>{hit.content}</pre>
        <footer>
          <span><i className="score-dot path-lex"/>{t('Lexical')} {hit.lexical_score.toFixed(2)}</span>
          <span><i className="score-dot path-str"/>{t('Path')} {hit.path_score.toFixed(2)}</span>
          <span><i className="score-dot path-str"/>{t('Symbol')} {hit.symbol_score.toFixed(2)}</span>
          <span><i className="score-dot path-sem"/>{t('Semantic')} {(hit.semantic_score||0).toFixed(2)}</span>
          {!!hit.rerank_score&&<span><i className="score-dot score-dot-rr"/>{t('Rerank')} {hit.rerank_score.toFixed(2)}</span>}
        </footer>
      </article>)}</div>
      :<pre className="ace-output">{result.formatted_retrieval}</pre>}
  </>
}
