import { useEffect, useState } from 'react'
import { Button, Loading } from '@geist-ui/core'
import { Box, Info } from '@geist-ui/icons'
import { api } from '../api'
import type { AceProject } from '../types'
import { useI18n } from '../i18n'
import { PageHeader } from '../components/PageHeader'
import { Empty } from '../components/Empty'
import { Pagination } from '../components/Pagination'
import { ConfirmModal } from '../components/ConfirmModal'
import { PAGE_SIZE, usePaged } from '../hooks/usePaged'
import { bytes, compact, formatDate, short } from '../lib/format'

// RepositoriesPage lists ACE projects: the user's own (with index status and
// delete) or, with `admin`, every project across users (with coverage and
// checkpoint columns).
export function RepositoriesPage({admin=false,refresh}:{admin?:boolean;refresh:number}){
  const {t,language}=useI18n()
  const [projects,setProjects]=useState<AceProject[]|null>(null)
  const {page,setPage,paged,total,size}=usePaged(projects||[],admin?PAGE_SIZE:5)
  const load=()=>api<{projects:AceProject[]}>(admin?'/api/v1/admin/ace-projects':'/api/v1/me/ace-projects').then(v=>setProjects(v.projects)).catch(()=>setProjects([]))
  useEffect(()=>{void load()},[admin,refresh])
  const [pendingDelete,setPendingDelete]=useState<AceProject|null>(null)
  const [deleting,setDeleting]=useState(false)
  const [deleteError,setDeleteError]=useState('')
  const closeDelete=()=>{if(deleting)return;setPendingDelete(null);setDeleteError('')}
  const confirmDelete=async()=>{
    if(!pendingDelete)return
    setDeleting(true);setDeleteError('')
    try{await api(`/api/v1/me/ace-projects/${encodeURIComponent(pendingDelete.name)}`,{method:'DELETE'});setPendingDelete(null);load()}
    catch(e){setDeleteError((e as Error).message)}
    finally{setDeleting(false)}
  }
  // Empty snapshot_id = placeholder row while the first upload is in flight.
  const indexStatus=(p:AceProject)=>
    !p.snapshot_id?<span className="status-pill warn"><i className="status-dot warn"/><em>{t('Uploading')}</em></span>
    :!p.chunk_count?<>—</>
    :p.embedded_count>=p.chunk_count?<span className="status-pill ok"><i className="status-dot ok"/><em>{t('Ready')}</em></span>
    :<span className="status-pill warn"><i className="status-dot warn"/><em>{t('Indexing')} {Math.round(p.embedded_count/p.chunk_count*100)}%</em></span>
  if(!projects)return <Loading/>
  return <>
    <PageHeader title={t(admin?'All projects':'My projects')} description={t(admin?'Every BCE-indexed project across users.':'Projects indexed by your BCE / MCP client. Names are inferred from project manifests.')}/>
    {!admin&&<div className="page-hint"><Info size={14}/><span>{t('The first retrieval of each project indexes it in the background; larger projects take longer, please be patient.')}</span></div>}
    <div className="data-table-wrap" role="region" tabIndex={0}>
      <table className="data-table">
        <thead><tr>
          <th>{t('Project')}</th>
          {admin&&<th>{t('Owner')}</th>}
          <th>{t('Files')}</th>
          <th>{t('Context chunks')}</th>
          {admin?<><th>{t('Vector coverage')}</th><th>{t('Storage')}</th><th>{t('Checkpoint')}</th></>:<><th>{t('Index status')}</th><th>{t('Storage')}</th></>}
          <th>{t('Last active')}</th>
          {!admin&&<th/>}
        </tr></thead>
        <tbody>{paged.map(p=><tr key={(p.owner_id||'')+p.name}>
          <td><div className="repo-name"><span className="repo-icon"><Box size={15}/></span><strong>{p.name}</strong></div></td>
          {admin&&<td>{p.owner_username}</td>}
          <td>{compact(p.file_count)}</td>
          <td>{compact(p.chunk_count)}</td>
          {admin
            ?<><td>{p.chunk_count?`${Math.round(p.embedded_count/p.chunk_count*100)}%`:'—'}</td><td>{bytes(p.storage_bytes)}</td><td><code className="inline-code">{short(p.snapshot_id)}</code></td></>
            :<><td>{indexStatus(p)}</td><td>{bytes(p.storage_bytes)}</td></>}
          <td>{formatDate(p.updated_at,language)}</td>
          {!admin&&<td><div className="row-actions"><Button auto scale={.62} type="error" ghost onClick={()=>setPendingDelete(p)}>{t('Delete')}</Button></div></td>}
        </tr>)}</tbody>
      </table>
      {projects.length===0&&<Empty title={t('No BCE projects yet.')} text={t('Run a search from your MCP client to index one.')}/>}
    </div>
    <Pagination total={total} page={page} onChange={setPage} size={size}/>
    {pendingDelete&&<ConfirmModal
      title={t('Delete project')} target={pendingDelete.name}
      text={t('All of its index data (snapshots, fragments, vectors) will be permanently erased and cannot be recovered; retrieval stops working until your client re-uploads the workspace on its next search.')}
      confirmLabel={t('Delete')} danger busy={deleting} error={deleteError}
      onCancel={closeDelete} onConfirm={confirmDelete}/>}
  </>
}
