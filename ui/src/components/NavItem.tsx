import { Archive } from '@geist-ui/icons'
import { useI18n } from '../i18n'

export function NavItem({label,icon:Icon,active,onClick}:{label:string;icon:typeof Archive;active:boolean;onClick:()=>void}){
  const {t}=useI18n()
  return <button className={`nav-item ${active?'active':''}`} onClick={onClick} title={t(label)} aria-current={active?'page':undefined}>
    <Icon size={15}/><span>{t(label)}</span>
  </button>
}
