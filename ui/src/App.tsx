import { useEffect, useState } from 'react'
import { Button, Loading } from '@geist-ui/core'
import { Bell, BookOpen, LogOut, Menu, Terminal } from '@geist-ui/icons'
import { api, jsonBody, setCSRF } from './api'
import type { Announcement, AuthConfig } from './types'
import { BrandLogo } from './BrandLogo'
import { Markdown } from './Markdown'
import { useI18n } from './i18n'
import { adminNav, allNav, DOCS_URL, userNav } from './nav'
import type { AuthState, Page } from './nav'
import { formatDate } from './lib/format'
import { NavItem } from './components/NavItem'
import { LangThemeSwitch } from './components/LangThemeSwitch'
import { McpMark } from './components/ToolMarks'
import { Landing } from './site/Landing'
import { McpPage } from './site/McpPage'
import { SkillPage } from './site/SkillPage'
import { Login } from './auth/Login'
import { Register } from './auth/Register'
import { PageContent } from './pages/PageContent'

type Entry='landing'|'login'|'mcp'|'skill'|'register'

// App is the shell: session + CSRF bootstrap, hash routing (public screens
// when signed out, console pages when signed in), theme persistence, the SSE
// refresh signal and the post-sign-in announcement popup.
export default function App(){
  const {t,language}=useI18n()
  const [auth,setAuth] = useState<AuthState|null>(null)
  const [loading,setLoading] = useState(true)
  const [page,setPage] = useState<Page>(() => {const h=location.hash.slice(1) as Page;return allNav.some(i=>i.page===h)?h:'overview'})
  const [dark,setDark] = useState(() => localStorage.getItem('bce-theme')==='dark')
  const [refresh,setRefresh] = useState(0)
  const [mobileNav,setMobileNav] = useState(false)
  // Signed-out routing: the public landing page is the default; '#login',
  // '#register', '#mcp' and '#skill' show their screens, so the browser back
  // button works between them.
  const parseEntry=():Entry=>location.hash==='#login'?'login':location.hash==='#mcp'?'mcp':location.hash==='#skill'?'skill':location.hash==='#register'?'register':'landing'
  const [entry,setEntry] = useState<Entry>(parseEntry)
  // Which sign-in extras exist (registration, Turnstile, LinuxDo) depends on
  // deployment configuration; entry points hide until this arrives.
  const [authCfg,setAuthCfg] = useState<AuthConfig|null>(null)
  // Latest undismissed announcement, shown once as a popup after sign-in;
  // dismissing it is recorded server-side until a newer one is published.
  const [announcement,setAnnouncement]=useState<Announcement|null>(null)
  useEffect(()=>{api<AuthState>('/api/v1/me').then(v=>{setCSRF(v.csrf_token);setAuth(v)}).catch(()=>setAuth(null)).finally(()=>setLoading(false))},[])
  useEffect(()=>{if(!auth){setAnnouncement(null);return};api<{announcement:Announcement|null}>('/api/v1/me/announcement').then(v=>setAnnouncement(v.announcement)).catch(()=>{})},[auth])
  const dismissAnnouncement=()=>{if(!announcement)return;const id=announcement.id;setAnnouncement(null);api('/api/v1/me/announcement/dismiss',{method:'POST',...jsonBody({id})}).catch(()=>{})}
  // Topbar bell: reopen the latest announcement on demand — ?latest=1 skips
  // the dismissal filter, since dismissing only silences the sign-in popup.
  const showLatestAnnouncement=()=>{api<{announcement:Announcement|null}>('/api/v1/me/announcement?latest=1').then(v=>{if(v.announcement)setAnnouncement(v.announcement)}).catch(()=>{})}
  useEffect(()=>{api<AuthConfig>('/api/v1/auth/config').then(setAuthCfg).catch(()=>{})},[])
  useEffect(()=>{const onHash=()=>setEntry(parseEntry());window.addEventListener('hashchange',onHash);return()=>window.removeEventListener('hashchange',onHash)},[])
  useEffect(()=>{document.documentElement.dataset.theme=dark?'dark':'light';localStorage.setItem('bce-theme',dark?'dark':'light')},[dark])
  useEffect(()=>{if(!auth)return;const stream=new EventSource('/api/v1/events');const update=()=>setRefresh(v=>v+1);['repository.updated','index.completed','index.failed','latency.updated','project.pending'].forEach(e=>stream.addEventListener(e,update));return()=>stream.close()},[auth])
  const toggleTheme=()=>setDark(v=>!v)
  const signIn=(v:AuthState)=>{setCSRF(v.csrf_token);setAuth(v)}
  const navigate=(next:Page)=>{location.hash=next;setPage(next);setMobileNav(false)}
  const logout=async()=>{await api('/api/v1/auth/logout',{method:'POST'}).catch(()=>{});location.hash='';setEntry('landing');setAuth(null)}
  if(loading)return <div className="center"><Loading>{t('Live index telemetry')}</Loading></div>
  if(!auth)return entry==='login'
    ?<Login onLogin={signIn} onBack={()=>{location.hash='';setEntry('landing')}} onRegister={()=>{location.hash='register';setEntry('register')}} cfg={authCfg} dark={dark} toggleTheme={toggleTheme}/>
    :entry==='register'
    ?<Register onLogin={signIn} onBack={()=>{location.hash='login';setEntry('login')}} cfg={authCfg} dark={dark} toggleTheme={toggleTheme}/>
    :entry==='mcp'
    ?<McpPage dark={dark} toggleTheme={toggleTheme}/>
    :entry==='skill'
    ?<SkillPage dark={dark} toggleTheme={toggleTheme}/>
    :<Landing onSignIn={()=>{location.hash='login';setEntry('login')}} dark={dark} toggleTheme={toggleTheme}/>
  // Signed-in users can still open '#mcp' / '#skill' (e.g. from the console
  // topbar buttons, which open a new tab): show the public page, not the console.
  if(entry==='mcp')return <McpPage dark={dark} toggleTheme={toggleTheme}/>
  if(entry==='skill')return <SkillPage dark={dark} toggleTheme={toggleTheme}/>
  const current=allNav.find(i=>i.page===page)
  const section=adminNav.some(i=>i.page===page)?'Administration':'Workspace'
  return <div className="app-shell">
    {mobileNav&&<button className="mobile-backdrop" aria-label={t('Close navigation')} onClick={()=>setMobileNav(false)}/>}
    <aside className={`sidebar ${mobileNav?'mobile-open':''}`} aria-label={t('Mobile navigation')}>
      <div className="brand"><BrandLogo size={22}/><div><strong>BCE{authCfg?.version&&<span className="brand-version">{authCfg.version}</span>}</strong><small>BETTER CONTEXT ENGINE</small></div></div>
      <div className="sidebar-nav">
        <div className="nav-section"><span>{t('Workspace')}</span>{userNav.map(item=><NavItem key={item.page} {...item} active={page===item.page} onClick={()=>navigate(item.page)}/>)}</div>
        {auth.user.role==='admin'&&<div className="nav-section"><span>{t('Administration')}</span>{adminNav.map(item=><NavItem key={item.page} {...item} active={page===item.page} onClick={()=>navigate(item.page)}/>)}</div>}
      </div>
      {/* LinuxDo 用户显示论坛原名 + 真实头像 + 等级徽章（本地用户名可能带重名后缀，如 bifang-2） */}
      <div className="sidebar-footer">
        <div className="identity">
          <span className="avatar">{auth.user.avatar_url?<img src={auth.user.avatar_url} alt=""/>:auth.user.username.slice(0,2).toUpperCase()}</span>
          <div>
            <strong>{auth.user.linuxdo_username||auth.user.username}{auth.user.linuxdo_id!=null&&auth.user.trust_level!=null&&<span className="trust-badge">Lv{auth.user.trust_level}</span>}</strong>
            <small>{t(auth.user.role)}</small>
          </div>
        </div>
        <button className="icon-button" onClick={logout} aria-label={t('Sign out')}><LogOut size={16}/></button>
      </div>
    </aside>
    <main className="main">
      <header className="topbar">
        <div className="topbar-left">
          <button className="icon-button mobile-menu" onClick={()=>setMobileNav(true)} aria-label={t('Open navigation')}><Menu size={18}/></button>
          <nav className="crumb" aria-label={t('Current page')}><span>{t(section)}</span><span className="crumb-sep" aria-hidden="true">/</span><strong>{t(current?.label??'Overview')}</strong></nav>
        </div>
        <div className="top-actions">
          <div className="live-state" title={t('Live index telemetry')}><span className="pulse"/><span className="live-label">{t('Live')}</span></div>
          <button className="icon-button" onClick={showLatestAnnouncement} aria-label={t('View latest announcement')} title={t('View latest announcement')}><Bell size={16}/></button>
          <button className="icon-button" onClick={()=>window.open(DOCS_URL,'_blank','noopener')} aria-label={t('Open documentation')} title={t('Open documentation')}><BookOpen size={17}/></button>
          <button className="icon-button" onClick={()=>window.open('#mcp','_blank','noopener')} aria-label={t('MCP server page')} title={t('MCP server page')}><McpMark/></button>
          <button className="icon-button" onClick={()=>window.open('#skill','_blank','noopener')} aria-label={t('Agent Skill page')} title={t('Agent Skill page')}><Terminal size={17}/></button>
          <LangThemeSwitch dark={dark} toggleTheme={toggleTheme}/>
        </div>
      </header>
      <div className={`page-container page-${page}`}><PageContent page={page} auth={auth} refresh={refresh}/></div>
    </main>
    {announcement&&<div className="modal-backdrop"><div className="modal-card announcement-card" role="dialog" aria-modal="true" aria-label={t('Announcement')}>
      <span className="announcement-kicker"><Bell size={13}/>{t('Announcement')}</span>
      <h3>{announcement.title}</h3>
      <div className="announcement-body"><Markdown source={announcement.content}/></div>
      <div className="modal-actions announcement-actions"><small>{formatDate(announcement.created_at,language)}</small><Button auto scale={.8} type="secondary" onClick={dismissAnnouncement}>{t('Got it')}</Button></div>
    </div></div>}
  </div>
}
