import { Moon, Sun } from '@geist-ui/icons'
import { useI18n } from '../i18n'

// LangThemeSwitch is the language + theme toggle pair shared by the public
// screens' glass nav and the console top bar.
export function LangThemeSwitch({dark,toggleTheme}:{dark:boolean;toggleTheme:()=>void}){
  const {t,language,toggleLanguage}=useI18n()
  return <>
    <button className="language-switch" onClick={toggleLanguage} aria-label={t('Switch language')}>{language==='zh'?'EN':'中文'}</button>
    <button className="icon-button" onClick={toggleTheme} aria-label={t('Toggle theme')}>{dark?<Sun size={17}/>:<Moon size={17}/>}</button>
  </>
}
