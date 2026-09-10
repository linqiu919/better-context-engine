import { useI18n } from '../i18n'

export function Status({value}:{value:string}){
  const {t}=useI18n()
  const tone =
    value==='healthy'||value==='success'||value==='configured'||value==='completed'?'ok':
    value==='failed'||value==='error'?'bad':
    value==='lagging'||value==='queued'||value==='fallback'||value==='warning'||value==='indexing'||value==='running'?'warn':
    value==='disabled'||value==='paused'||value==='cancelled'||value==='not_configured'||value==='planned'?'muted':
    'neutral'
  return <span className={`status-pill ${tone}`}><i className={`status-dot ${tone==='muted'||tone==='neutral'?'':tone}`}/><em>{t(value)}</em></span>
}
