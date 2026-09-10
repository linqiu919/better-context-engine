import { useState } from 'react'
import { BrandLogo } from '../BrandLogo'
import { useI18n } from '../i18n'
import { LangThemeSwitch } from '../components/LangThemeSwitch'
import { PublicNav } from '../site/PublicChrome'

// LoginShell is the shared chrome of the sign-in and registration screens:
// the same floating glass top bar as the landing/MCP pages (brand doubles as
// back-to-home, language and theme switches), centered card below.
export function LoginShell({onBack,dark,toggleTheme,onSubmit,children}:{onBack:()=>void;dark:boolean;toggleTheme:()=>void;onSubmit:(e:React.FormEvent)=>void;children:React.ReactNode}){
  const {t}=useI18n()
  const [scrolled,setScrolled]=useState(false)
  return <div className="login-page" onScroll={e=>setScrolled(e.currentTarget.scrollTop>24)}>
    <PublicNav scrolled={scrolled} onBrand={onBack} brandLabel={t('Back to home')}>
      <LangThemeSwitch dark={dark} toggleTheme={toggleTheme}/>
    </PublicNav>
    <form className="login-panel" onSubmit={onSubmit}>
      <div className="login-brandlock"><BrandLogo size={30}/><small>BETTER CONTEXT ENGINE</small></div>
      <div className="login-card">
        <div className="login-fields">{children}</div>
      </div>
    </form>
  </div>
}
