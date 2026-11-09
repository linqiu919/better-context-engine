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
import { ToolGroup } from './components/ToolGroup'
import { useGlide } from './hooks/useGlide'
import { LangThemeSwitch } from './components/LangThemeSwitch'
import { DeepSeekLink, McpMark } from './components/ToolMarks'
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
  // Console page follows the hash both ways: navigate() writes it and the
  // hashchange listener reads it back, so Back/Forward switch pages too.
  const parsePage=():Page|null=>{const h=location.hash.slice(1) as Page;return allNav.some(i=>i.page===h)?h:null}
  const [page,setPage] = useState<Page>(() => parsePage()??'overview')
  const [dark,setDark] = useState(() => localStorage.getItem('bce-theme')==='dark')
  const [refresh,setRefresh] = useState(0)
  const [mobileNav,setMobileNav] = useState(false)
  // SSE link state behind the topbar "Live" pill: connecting → open, and on
  // a dropped stream reconnecting (the browser retries by itself; a stream it
  // gave up on is recreated here with backoff).
  const [live,setLive] = useState<'connecting'|'open'|'reconnecting'>('connecting')
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
  useEffect(()=>{if(!auth){setAnnouncement(null);return};let alive=true;api<{announcement:Announcement|null}>('/api/v1/me/announcement').then(v=>{if(alive)setAnnouncement(v.announcement)}).catch(()=>{});return()=>{alive=false}},[auth])
  const dismissAnnouncement=()=>{if(!announcement)return;const id=announcement.id;setAnnouncement(null);api('/api/v1/me/announcement/dismiss',{method:'POST',...jsonBody({id})}).catch(()=>{})}
  // Topbar bell: reopen the latest announcement on demand — ?latest=1 skips
  // the dismissal filter, since dismissing only silences the sign-in popup.
  const showLatestAnnouncement=()=>{api<{announcement:Announcement|null}>('/api/v1/me/announcement?latest=1').then(v=>{if(v.announcement)setAnnouncement(v.announcement)}).catch(()=>{})}
  useEffect(()=>{api<AuthConfig>('/api/v1/auth/config').then(setAuthCfg).catch(()=>{})},[])
  useEffect(()=>{const onHash=()=>{setEntry(parseEntry());const p=parsePage();if(p)setPage(p)};window.addEventListener('hashchange',onHash);return()=>window.removeEventListener('hashchange',onHash)},[])
  useEffect(()=>{document.documentElement.dataset.theme=dark?'dark':'light';localStorage.setItem('bce-theme',dark?'dark':'light')},[dark])
  // Every search publishes latency.updated, so on a busy server it would
  // reload the open list page several times a second; it is coalesced to at
  // most one refresh per 15s (trailing, so the last search still shows).
  // Indexing and project events stay immediate. Events sent while the stream
  // was down are not replayed, so every reconnect triggers one refresh.
  useEffect(()=>{
    if(!auth)return
    setLive('connecting')
    const update=()=>setRefresh(v=>v+1);let last=0;let timer:number|undefined
    const throttled=()=>{if(timer!==undefined)return;const wait=Math.max(0,last+15000-Date.now());timer=window.setTimeout(()=>{timer=undefined;last=Date.now();update()},wait)}
    let stream:EventSource|undefined,retry:number|undefined,backoff=1000,opened=false,disposed=false
    const connect=()=>{
      const s=stream=new EventSource('/api/v1/events')
      s.onopen=()=>{setLive('open');backoff=1000;if(opened)update();opened=true}
      s.onerror=()=>{
        if(disposed)return
        setLive('reconnecting')
        if(s.readyState===EventSource.CLOSED){s.close();retry=window.setTimeout(connect,backoff);backoff=Math.min(backoff*2,30000)}
      }
      for(const e of ['repository.updated','index.completed','index.failed','project.pending'])s.addEventListener(e,update)
      s.addEventListener('latency.updated',throttled)
    }
    connect()
    return()=>{disposed=true;stream?.close();window.clearTimeout(retry);if(timer!==undefined)window.clearTimeout(timer)}
  },[auth])
  // One active rail + hover fill slides across both nav sections. Called
  // before the early returns; the deps re-measure once the shell mounts.
  const navGlide=useGlide<HTMLDivElement>('.nav-item',[page,auth?.user.role,entry,loading,language])
  const toggleTheme=()=>setDark(v=>!v)
  // Sign-in leaves the #login/#register screen behind (replace, so Back does
  // not return to a sign-in form); sign-out drops the CSRF token and the
  // console page so the next account starts on the overview.
  const signIn=(v:AuthState)=>{setCSRF(v.csrf_token);if(entry==='login'||entry==='register'){history.replaceState(null,'',location.pathname+location.search+'#'+page);setEntry('landing')};setAuth(v)}
  const navigate=(next:Page)=>{location.hash=next;setPage(next);setMobileNav(false)}
  const logout=async()=>{await api('/api/v1/auth/logout',{method:'POST'}).catch(()=>{});setCSRF('');location.hash='';setEntry('landing');setPage('overview');setAuth(null)}
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
  // Admin pages are only reachable for admins: a non-admin arriving on one
  // (typed or restored hash) sees the overview instead.
  const shown:Page=auth.user.role!=='admin'&&adminNav.some(i=>i.page===page)?'overview':page
  const current=allNav.find(i=>i.page===shown)
  const section=adminNav.some(i=>i.page===shown)?'Administration':'Workspace'
  return <div className="app-shell">
    {mobileNav&&<button className="mobile-backdrop" aria-label={t('Close navigation')} onClick={()=>setMobileNav(false)}/>}
    <aside className={`sidebar ${mobileNav?'mobile-open':''}`} aria-label={t('Mobile navigation')}>
      <div className="brand"><BrandLogo size={22}/><div><strong>BCE{authCfg?.version&&<span className="brand-version">{authCfg.version}</span>}</strong><small>BETTER CONTEXT ENGINE</small></div></div>
      <div className="sidebar-nav glide" ref={navGlide.ref} {...navGlide.handlers}>
        {navGlide.layer}
        <div className="nav-section"><span>{t('Workspace')}</span>{userNav.map(item=><NavItem key={item.page} {...item} active={shown===item.page} onClick={()=>navigate(item.page)}/>)}</div>
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
          <div className={`live-state ${live==='open'?'':'offline'}`} title={t('Live index telemetry')} role="status"><span className="pulse"/><span className="live-label">{t(live==='open'?'Live':live==='connecting'?'Connecting':'Reconnecting')}</span></div>
          <ToolGroup>
            <button className="icon-button" onClick={showLatestAnnouncement} aria-label={t('View latest announcement')}><Bell size={16}/></button>
            <button className="icon-button" onClick={()=>window.open(DOCS_URL,'_blank','noopener')} aria-label={t('Open documentation')}><BookOpen size={17}/></button>
            <button className="icon-button" onClick={()=>window.open('#mcp','_blank','noopener')} aria-label={t('MCP server page')}><McpMark/></button>
            <button className="icon-button" onClick={()=>window.open('#skill','_blank','noopener')} aria-label={t('Agent Skill page')}><Terminal size={17}/></button>
            <DeepSeekLink label={t('DeepSeek project')}/>
            <LangThemeSwitch dark={dark} toggleTheme={toggleTheme}/>
          </ToolGroup>
        </div>
      </header>
      <div className={`page-container page-${shown}`}><PageContent page={shown} auth={auth} refresh={refresh}/></div>
    </main>
    {announcement&&<div className="modal-backdrop"><div className="modal-card announcement-card" role="dialog" aria-modal="true" aria-label={t('Announcement')}>
      <span className="announcement-kicker"><Bell size={13}/>{t('Announcement')}</span>
      <h3>{announcement.title}</h3>
      <div className="announcement-body"><Markdown source={announcement.content}/></div>
      <div className="modal-actions announcement-actions"><small>{formatDate(announcement.created_at,language)}</small><Button auto scale={.8} type="secondary" onClick={dismissAnnouncement}>{t('Got it')}</Button></div>
    </div></div>}
  </div>
}
