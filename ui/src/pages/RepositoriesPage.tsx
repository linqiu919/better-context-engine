import { lazy, Suspense, useMemo, useState } from 'react'
import { Button, Loading } from '@geist-ui/core'
import { Box, GitBranch, Info, Search, Share2, Trash2 } from '@geist-ui/icons'
import { api } from '../api'
import type { AceProject } from '../types'
import { useI18n } from '../i18n'
import { PageHeader } from '../components/PageHeader'
import { Empty } from '../components/Empty'
import { Pagination } from '../components/Pagination'
import { ConfirmModal } from '../components/ConfirmModal'
import { usePaged } from '../hooks/usePaged'
import { useLeaving } from '../hooks/useLeaving'
import { useFitRows } from '../hooks/useFitRows'
import { useResource } from '../hooks/useResource'
import { LoadError } from '../components/LoadError'
import { indexState } from '../lib/indexState'
import { bytes, compact, formatDate, projectLabel } from '../lib/format'

// Lazy: force-graph (canvas + d3 modules) only downloads when a graph opens.
const ProjectGraphModal = lazy(() => import('../components/ProjectGraphModal'))

// RepositoriesPage lists ACE projects: the user's own (with index status and
// delete) or, with `admin`, every project across users (with owner and
// coverage columns). PageContent keys the two variants apart, so switching
// between them remounts instead of showing one list under the other's columns.
export function RepositoriesPage({admin=false,refresh}:{admin?:boolean;refresh:number}){
  const {t,language}=useI18n()
  // SSE refreshes reload in place; a failed reload keeps the current rows and
  // shows the error, rather than wiping the table into "no projects yet".
  const list=useResource(()=>api<{projects:AceProject[]}>(admin?'/api/v1/admin/ace-projects':'/api/v1/me/ace-projects').then(v=>v.projects),[admin,refresh])
  const projects=list.data,setProjects=list.setData
  // Client-side fuzzy name filter: the project list is already fully loaded,
  // so a case-insensitive substring match over it is instant and needs no API.
  const [query,setQuery]=useState('')
  const filtered=useMemo(()=>{
    const q=query.trim().toLowerCase()
    if(!q)return projects||[]
    return (projects||[]).filter(p=>p.name.toLowerCase().includes(q)||(p.branch||'').toLowerCase().includes(q))
  },[projects,query])
  const rows=useLeaving<HTMLTableSectionElement>()
  // Page size follows the viewport height so the table fills the pane without a scrollbar.
  const fit=useFitRows(rows.ref,filtered.length)
  const {page,setPage,paged,total,size}=usePaged(filtered,fit)
  const [graphProject,setGraphProject]=useState<AceProject|null>(null)
  const [pendingDelete,setPendingDelete]=useState<AceProject|null>(null)
  const [deleting,setDeleting]=useState(false)
  const [deleteError,setDeleteError]=useState('')
  const rowKey=(p:AceProject)=>(p.owner_id||'')+p.name+'\n'+(p.branch||'')
  const closeDelete=()=>{if(deleting)return;setPendingDelete(null);setDeleteError('')}
  const confirmDelete=async()=>{
    if(!pendingDelete||deleting)return
    setDeleting(true);setDeleteError('')
    const gone=rowKey(pendingDelete)
    try{
      await api(projectPath(pendingDelete),{method:'DELETE'});setPendingDelete(null)
      rows.leave(gone,()=>{setProjects(ps=>ps&&ps.filter(p=>rowKey(p)!==gone));list.reload()})
    }
    catch(e){setDeleteError((e as Error).message)}
    finally{setDeleting(false)}
  }
  // Rows are keyed by (name, branch); branch rides along as a query param.
  const projectPath=(p:AceProject)=>`/api/v1/me/ace-projects/${encodeURIComponent(p.name)}${p.branch?`?branch=${encodeURIComponent(p.branch)}`:''}`
  // Empty snapshot_id = placeholder row while the first upload is in flight.
  const indexStatus=(p:AceProject)=>{
    const s=indexState(p)
    switch(s.kind){
      case'uploading':return <span className="status-pill warn"><i className="status-dot warn"/><em>{t('Uploading')}</em></span>
      case'empty':return <>—</>
      case'ready':return <span className="status-pill ok"><i className="status-dot ok"/><em>{t('Ready')}</em></span>
      case'indexing':return <span className="status-pill warn"><i className="status-dot warn"/><em>{t('Indexing')} {s.pct}%</em></span>
    }
  }
  if(!projects)return list.error?<LoadError error={list.error} onRetry={list.reload}/>:<Loading/>
  return <>
    <PageHeader title={t(admin?'All projects':'My projects')} description={t(admin?'Every BCE-indexed project across users.':'Projects indexed by your BCE / MCP client. Names are inferred from project manifests.')}/>
    <div className="page-toolbar">
      {!admin&&<div className="page-hint"><Info size={14}/><span>{t('The first retrieval of each project indexes it in the background; larger projects take longer, please be patient.')}</span></div>}
      <label className="table-search">
        <Search size={13}/>
        <input value={query} onChange={e=>{setQuery(e.target.value);setPage(1)}} placeholder={t('Filter by project name')} aria-label={t('Filter by project name')}/>
      </label>
    </div>
    {list.error&&<LoadError error={list.error} onRetry={list.reload}/>}
    <div className="data-table-wrap" role="region" tabIndex={0}>
      <table className="data-table">
        <thead><tr>
          <th>{t('Project')}</th>
          {admin&&<th>{t('Owner')}</th>}
          <th>{t('Files')}</th>
          <th>{t('Context chunks')}</th>
          {admin?<><th>{t('Vector coverage')}</th><th>{t('Storage')}</th></>:<><th>{t('Index status')}</th><th>{t('Storage')}</th></>}
          <th>{t('Last active')}</th>
          {!admin&&<th/>}
        </tr></thead>
        <tbody ref={rows.ref}>{paged.map(p=><tr key={rowKey(p)} data-key={rowKey(p)} className={rows.leaving===rowKey(p)?'row-leaving':undefined}>
          <td><div className="repo-name"><span className="repo-icon"><Box size={15}/></span><div>
            <strong>{p.name}</strong>
            {(p.branch||p.worktree)&&<small className="repo-meta">
              {p.branch&&<><GitBranch size={10}/><span>{p.branch}</span></>}
              {p.worktree&&<span className="worktree-badge" title={t('Linked git worktree')}>worktree</span>}
            </small>}
          </div></div></td>
          {admin&&<td>{p.owner_username}</td>}
          <td>{compact(p.file_count)}</td>
          <td>{compact(p.chunk_count)}</td>
          {admin
            ?<><td>{p.chunk_count?`${Math.floor(p.embedded_count/p.chunk_count*100)}%`:'—'}</td><td>{bytes(p.storage_bytes)}</td></>
            :<><td>{indexStatus(p)}</td><td>{bytes(p.storage_bytes)}</td></>}
          <td>{formatDate(p.updated_at,language)}</td>
          {!admin&&<td><div className="row-actions">
            <Button auto scale={.62} ghost className="btn-graph" icon={<Share2/>} disabled={!p.snapshot_id} onClick={()=>setGraphProject(p)}>{t('Graph')}</Button>
            <Button auto scale={.62} type="error" ghost icon={<Trash2/>} disabled={rows.leaving===rowKey(p)} onClick={()=>setPendingDelete(p)}>{t('Delete')}</Button>
          </div></td>}
        </tr>)}</tbody>
      </table>
      {projects.length===0&&<Empty title={t('No BCE projects yet.')} text={t('Run a search from your MCP client to index one.')}/>}
      {projects.length>0&&filtered.length===0&&<Empty title={t('No projects match.')} text={t('Try a different keyword.')}/>}
    </div>
    <Pagination total={total} page={page} onChange={setPage} size={size}/>
    {graphProject&&<Suspense fallback={null}><ProjectGraphModal project={graphProject.name} branch={graphProject.branch} onClose={()=>setGraphProject(null)}/></Suspense>}
    {pendingDelete&&<ConfirmModal
      title={t('Delete project')} target={projectLabel(pendingDelete.name,pendingDelete.branch)}
      text={t('All of its index data (snapshots, fragments, vectors) will be permanently erased and cannot be recovered; retrieval stops working until your client re-uploads the workspace on its next search.')}
      confirmLabel={t('Delete')} danger busy={deleting} error={deleteError}
      onCancel={closeDelete} onConfirm={confirmDelete}/>}
  </>
}
