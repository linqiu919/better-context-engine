import { useEffect, useState } from 'react'
import { Button, Loading } from '@geist-ui/core'
import { Bell } from '@geist-ui/icons'
import { api, jsonBody } from '../api'
import type { Announcement } from '../types'
import { useI18n } from '../i18n'
import { Markdown } from '../Markdown'
import { PageHeader } from '../components/PageHeader'
import { Panel } from '../components/Panel'
import { Setting } from '../components/Setting'
import { Empty } from '../components/Empty'
import { Pagination } from '../components/Pagination'
import { ConfirmModal } from '../components/ConfirmModal'
import { usePaged } from '../hooks/usePaged'
import { formatDate } from '../lib/format'

// manages the history; users see the newest one as a popup after signing in.
export function AnnouncementsPage(){
  const {t,language}=useI18n()
  const [items,setItems]=useState<Announcement[]|null>(null)
  const [title,setTitle]=useState('')
  const [content,setContent]=useState('')
  const [tab,setTab]=useState<'write'|'preview'>('write')
  const [busy,setBusy]=useState(false)
  const [published,setPublished]=useState(false)
  const [error,setError]=useState('')
  const [viewing,setViewing]=useState<Announcement|null>(null)
  const [pendingDelete,setPendingDelete]=useState<Announcement|null>(null)
  const [deleting,setDeleting]=useState(false)
  const [deleteError,setDeleteError]=useState('')
  const pager=usePaged(items||[])
  const load=()=>api<{announcements:Announcement[]}>('/api/v1/admin/announcements').then(v=>setItems(v.announcements||[])).catch(()=>setItems([]))
  useEffect(()=>{void load()},[])
  const publish=async()=>{
    setBusy(true);setPublished(false);setError('')
    try{
      await api('/api/v1/admin/announcements',{method:'POST',...jsonBody({title:title.trim(),content})})
      setTitle('');setContent('');setTab('write');setPublished(true);load()
    }catch(e){setError((e as Error).message)}
    finally{setBusy(false)}
  }
  const confirmDelete=async()=>{
    if(!pendingDelete)return
    setDeleting(true);setDeleteError('')
    try{
      await api(`/api/v1/admin/announcements/${pendingDelete.id}`,{method:'DELETE'})
      setPendingDelete(null);load()
    }catch(e){setDeleteError((e as Error).message)}
    finally{setDeleting(false)}
  }
  return <>
    <PageHeader title={t('Announcements')} description={t('Publish Markdown announcements; users see the latest one as a popup after signing in.')}/>
    <Panel title={t('Publish announcement')} meta="ADMIN ONLY">
      <p className="settings-intro">{t('The popup shows once per user; dismissing it keeps it away until a newer announcement is published.')}</p>
      <div className="announce-form">
        <Setting label={t('Title')}><input value={title} onChange={e=>setTitle(e.target.value)} maxLength={200}/></Setting>
        <div className="announce-editor">
          <div className="tab-switch"><button className={tab==='write'?'active':''} onClick={()=>setTab('write')}>{t('Write')}</button><button className={tab==='preview'?'active':''} onClick={()=>setTab('preview')}>{t('Preview')}</button></div>
          {tab==='write'
            ?<textarea value={content} onChange={e=>setContent(e.target.value)} rows={10} placeholder={t('Markdown supported: # headings, **bold**, `code`, lists, links, quotes.')}/>
            :<div className="announce-preview">{content.trim()?<Markdown source={content}/>:<p className="settings-intro">{t('Nothing to preview yet.')}</p>}</div>}
        </div>
      </div>
      {error&&<p className="form-error">{error}</p>}
      <div className="settings-actions">{published&&<span>{t('Announcement published')}</span>}<Button auto type="secondary" loading={busy} disabled={!title.trim()||!content.trim()} onClick={publish}>{t('Publish')}</Button></div>
    </Panel>
    <Panel title={t('Published announcements')}>
      {!items?<Loading/>:items.length===0?<Empty title={t('No announcements yet.')} text={t('Published announcements will be listed here.')}/>:<>
        <div className="data-table-wrap" role="region" tabIndex={0}>
          <table className="data-table">
            <thead><tr><th>{t('Title')}</th><th>{t('Publisher')}</th><th>{t('Published at')}</th><th/></tr></thead>
            <tbody>{pager.paged.map(a=><tr key={a.id}>
              <td><strong>{a.title}</strong></td>
              <td>{a.author}</td>
              <td>{formatDate(a.created_at,language)}</td>
              <td><div className="row-actions"><Button auto scale={.62} ghost onClick={()=>setViewing(a)}>{t('View')}</Button><Button auto scale={.62} type="error" ghost onClick={()=>{setPendingDelete(a);setDeleteError('')}}>{t('Delete')}</Button></div></td>
            </tr>)}</tbody>
          </table>
        </div>
        <Pagination total={pager.total} page={pager.page} onChange={pager.setPage}/>
      </>}
    </Panel>
    {viewing&&<div className="modal-backdrop" onClick={()=>setViewing(null)}><div className="modal-card announcement-card" role="dialog" aria-modal="true" aria-label={t('Announcement')} onClick={e=>e.stopPropagation()}>
      <span className="announcement-kicker"><Bell size={13}/>{t('Announcement')}</span>
      <h3>{viewing.title}</h3>
      <div className="announcement-body"><Markdown source={viewing.content}/></div>
      <div className="modal-actions announcement-actions"><small>{viewing.author} · {formatDate(viewing.created_at,language)}</small><Button auto scale={.8} onClick={()=>setViewing(null)}>{t('Close')}</Button></div>
    </div></div>}
    {pendingDelete&&<ConfirmModal
      title={t('Delete announcement')} target={pendingDelete.title}
      text={t('Delete this announcement? Users who have not seen it yet will no longer get the popup.')}
      confirmLabel={t('Delete')} danger busy={deleting} error={deleteError}
      onCancel={()=>setPendingDelete(null)} onConfirm={confirmDelete}/>}
  </>
}
