import { useI18n } from '../i18n'

// Rotating-word slogan shared by the landing hero and the login brand panel:
// 更好/更准/更稳 的检索引擎 (the rotator echoes the project name, better-context-engine).
export function Slogan(){
  const {t,language}=useI18n()
  const words = language==='zh' ? ['更好','更准','更稳'] : ['better','sharper','steadier']
  return <h1 className="slogan">
    <span className="slogan-prefix">
      {language==='zh'?null:<span className="slogan-fix">A</span>}
      <span className="word-rotator"><span className="word-track">{[...words,words[0]].map((w,i)=><span key={i}>{w}</span>)}</span></span>
      {language==='zh'?<span className="slogan-fix">的</span>:null}
    </span>
    <br/><em>{t('context retrieval engine')}</em>
  </h1>
}
