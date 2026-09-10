import { useEffect } from 'react'
import { Button } from '@geist-ui/core'
import { useI18n } from '../i18n'

// ConfirmModal is the styled replacement for window.confirm (which is banned
// in this console): same .modal-* visual language as the announcement popup,
// with Escape/backdrop dismissal and a busy-guarded confirm action.
export function ConfirmModal({title,target,text,confirmLabel,danger,busy,error,onCancel,onConfirm}:{title:string;target?:string;text:string;confirmLabel:string;danger?:boolean;busy?:boolean;error?:string;onCancel:()=>void;onConfirm:()=>void}){
  const {t}=useI18n()
  useEffect(()=>{const onKey=(e:KeyboardEvent)=>{if(e.key==='Escape'&&!busy)onCancel()};document.addEventListener('keydown',onKey);return()=>document.removeEventListener('keydown',onKey)},[busy,onCancel])
  const cancel=()=>{if(!busy)onCancel()}
  return <div className="modal-backdrop" onClick={cancel}><div className="modal-card" role="dialog" aria-modal="true" aria-label={title} onClick={e=>e.stopPropagation()}>
    <h3>{title}</h3>
    {target&&<p className="modal-target">{target}</p>}
    <p className="modal-text">{text}</p>
    {error&&<p className="form-error">{error}</p>}
    <div className="modal-actions"><Button auto scale={.8} onClick={cancel} disabled={busy}>{t('Cancel')}</Button><Button auto scale={.8} type={danger?'error':'secondary'} loading={busy} onClick={onConfirm}>{confirmLabel}</Button></div>
  </div></div>
}
