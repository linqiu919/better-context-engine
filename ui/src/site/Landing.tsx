import { useEffect, useRef, useState } from 'react'
import { Button } from '@geist-ui/core'
import { ArrowDown, ArrowRight, ArrowUp, ArrowUpRight, BarChart2, BookOpen, Box, Database, Search, Terminal } from '@geist-ui/icons'
import { BrandLogo } from '../BrandLogo'
import { useI18n } from '../i18n'
import { DOCS_URL } from '../nav'
import { prefersReducedMotion } from '../lib/motion'
import { Slogan } from '../components/Slogan'
import { FusionArt } from '../components/FusionArt'
import { LangThemeSwitch } from '../components/LangThemeSwitch'
import { ClaudeMark, CodexMark, CursorMark, McpMark, VSCodeMark, ZedMark } from '../components/ToolMarks'
import { McpConfigPanel } from './McpConfigPanel'
import { PublicNav, SiteFooter } from './PublicChrome'

// The live-indexing screen rotates through three copy sets — one per system
// capability (live sync / incremental dedup / three-way indexing).
const liveCopySets = [
  { eyebrow: 'Live indexing', title: 'Your code, indexed as you save', lead: 'Every save becomes an incremental checkpoint: code is split into fragments while terms, symbols and vectors update in step — your next search always sees the latest code.' },
  { eyebrow: 'Incremental dedup', title: 'Only changes incur cost', lead: 'Checkpoints are content-addressed: unchanged fragments reuse the existing index, deduplicated across users and projects — repeated content never bills twice.' },
  { eyebrow: 'Three-way indexing', title: 'Every fragment, indexed three ways', lead: 'Lexical postings, structural symbols and semantic vectors — every code fragment enters all three indexes at once, ready for hybrid recall.' },
]

// Public landing page (signed-out default): scroll-snap screens for hero,
// live indexing, retrieval system, prompt enhancement, cost, MCP setup and CTA.
export function Landing({onSignIn,dark,toggleTheme}:{onSignIn:()=>void;dark:boolean;toggleTheme:()=>void}){
  const {t}=useI18n()
  const [scrolled,setScrolled]=useState(false)
  const [showTop,setShowTop]=useState(false)
  const [copySet,setCopySet]=useState(0)
  const pageRef=useRef<HTMLDivElement>(null)
  // Copy rotation is motion, so it is disabled under reduced-motion (the
  // first set then stays static); the swap animation itself is CSS-gated.
  useEffect(()=>{if(prefersReducedMotion())return;const id=window.setInterval(()=>setCopySet(i=>(i+1)%liveCopySets.length),5200);return()=>window.clearInterval(id)},[])
  // Anchor scroll stays inside the landing scroll container; location.hash is
  // reserved for login/landing routing, so sections are reached by id.
  const scrollToScreen=(id:string)=>{document.getElementById(id)?.scrollIntoView({behavior:prefersReducedMotion()?'auto':'smooth'})}
  const scrollToTop=()=>{pageRef.current?.scrollTo({top:0,behavior:prefersReducedMotion()?'auto':'smooth'})}
  return <div className="landing-page" ref={pageRef} onScroll={e=>{const top=e.currentTarget.scrollTop;setScrolled(top>24);setShowTop(top>560)}}>
    <PublicNav scrolled={scrolled} onBrand={scrollToTop} brandLabel={t('Back to top')}>
      <button className="icon-button" onClick={()=>window.open(DOCS_URL,'_blank','noopener')} aria-label={t('Open documentation')} title={t('Open documentation')}><BookOpen size={17}/></button>
      <button className="icon-button" onClick={()=>{location.hash='mcp'}} aria-label={t('MCP server page')} title={t('MCP server page')}><McpMark/></button>
      <button className="icon-button" onClick={()=>{location.hash='skill'}} aria-label={t('Agent Skill page')} title={t('Agent Skill page')}><Terminal size={17}/></button>
      <LangThemeSwitch dark={dark} toggleTheme={toggleTheme}/>
      <Button auto scale={0.75} type="secondary" onClick={onSignIn}>{t('Get started')}</Button>
    </PublicNav>
    <main>
      <section className="landing-screen">
        <div className="scroll-cue" aria-hidden="true"><span>{t('Scroll')}</span><i/></div>
        <div className="landing-hero">
          <div className="landing-hero-copy">
            <span className="hero-eyebrow"><i/>{t('ACE-compatible context engine')}</span>
            <Slogan/>
            <p>{t('Every good answer begins with the right context.')}</p>
            <div className="landing-cta"><Button auto type="secondary" className="brand-btn" onClick={onSignIn}>{t('Get started')}</Button><button type="button" className="ghost-link" onClick={()=>scrollToScreen('mcp-screen')}>{t('See how to connect')}<ArrowDown size={13}/></button></div>
          </div>
          <div className="hero-console" aria-hidden="true">
            <div className="win-chrome"><i/><i/><i/><span>{t('Retrieval sketch')}</span></div>
            <div className="console-query"><span className="console-dot"/><code>{t('"where is login handled" → internal/auth/middleware.go')}</code></div>
            <div className="console-fusion">
              <div className="fusion-labels"><span className="path-lex">{t('Lexical')}</span><span className="path-str">{t('Structural')}</span><span className="path-sem">{t('Semantic')}</span></div>
              <FusionArt/>
              <span className="fusion-answer">{t('Precise context')}</span>
            </div>
            <div className="console-hits">
              <div className="console-hit"><i className="path-lex"/><code>internal/auth/middleware.go</code></div>
              <div className="console-hit"><i className="path-str"/><code>internal/httpapi/server.go</code></div>
              <div className="console-hit"><i className="path-sem"/><code>ui/src/App.tsx</code></div>
            </div>
          </div>
        </div>
      </section>
      <section className="landing-screen" id="indexing-screen">
        <div className="landing-live">
          <div className="index-panel" aria-hidden="true">
            <div className="win-chrome"><i/><i/><i/><span>{t('Live indexing')}</span></div>
            <svg viewBox="0 0 560 300" className="index-scene">
              <path className="ilx-wire" d="M300 71 C330 71 350 70 380 70"/>
              <path className="ilx-wire" d="M300 159 C330 159 350 150 380 150"/>
              <path className="ilx-wire" d="M300 247 C330 247 350 230 380 230"/>
              <rect className="ilx-chunk lex" x="24" y="32" width="268" height="78" rx="8"/>
              <rect className="ilx-chunk str" x="24" y="120" width="268" height="78" rx="8"/>
              <rect className="ilx-chunk sem" x="24" y="208" width="268" height="78" rx="8"/>
              <g className="ilx-code">
                <rect x="40" y="44" width="168" height="8" rx="4"/><rect x="40" y="62" width="112" height="8" rx="4"/><rect x="40" y="80" width="196" height="8" rx="4"/><rect x="40" y="98" width="88" height="8" rx="4"/>
                <rect x="40" y="132" width="140" height="8" rx="4"/><rect x="40" y="150" width="208" height="8" rx="4"/><rect x="40" y="168" width="96" height="8" rx="4"/><rect x="40" y="186" width="172" height="8" rx="4"/>
                <rect x="40" y="220" width="124" height="8" rx="4"/><rect x="40" y="238" width="188" height="8" rx="4"/><rect x="40" y="256" width="80" height="8" rx="4"/><rect x="40" y="274" width="152" height="8" rx="4"/>
              </g>
              <rect className="ilx-chip" x="380" y="52" width="164" height="36" rx="8"/>
              <rect className="ilx-chip" x="380" y="132" width="164" height="36" rx="8"/>
              <rect className="ilx-chip" x="380" y="212" width="164" height="36" rx="8"/>
              <text className="ilx-chip-text lex" x="398" y="75">LEXICAL</text>
              <text className="ilx-chip-text str" x="398" y="155">STRUCTURAL</text>
              <text className="ilx-chip-text sem" x="398" y="235">SEMANTIC</text>
              <g className="ilx-anim">
                <rect className="ilx-scan" x="20" y="22" width="276" height="2" rx="1"><animateTransform attributeName="transform" type="translate" values="0 0;0 264" dur="6s" repeatCount="indefinite"/></rect>
                <rect className="ilx-pulse lex" x="24" y="32" width="268" height="78" rx="8" opacity="0"><animate attributeName="opacity" values="0;.14;.14;0;0" keyTimes="0;0.04;0.24;0.28;1" dur="6s" repeatCount="indefinite"/></rect>
                <rect className="ilx-pulse str" x="24" y="120" width="268" height="78" rx="8" opacity="0"><animate attributeName="opacity" values="0;.14;.14;0;0" keyTimes="0;0.36;0.56;0.60;1" dur="6s" repeatCount="indefinite"/></rect>
                <rect className="ilx-pulse sem" x="24" y="208" width="268" height="78" rx="8" opacity="0"><animate attributeName="opacity" values="0;.14;.14;0;0" keyTimes="0;0.68;0.88;0.92;1" dur="6s" repeatCount="indefinite"/></rect>
                <circle className="ilx-dot lex" r="4.5" opacity="0">
                  <animateMotion path="M300 71 C330 71 350 70 380 70" dur="6s" repeatCount="indefinite" calcMode="linear" keyPoints="0;0;1;1" keyTimes="0;0.14;0.28;1"/>
                  <animate attributeName="opacity" values="0;1;1;0;0" keyTimes="0;0.14;0.27;0.30;1" dur="6s" repeatCount="indefinite"/>
                </circle>
                <circle className="ilx-dot str" r="4.5" opacity="0">
                  <animateMotion path="M300 159 C330 159 350 150 380 150" dur="6s" repeatCount="indefinite" calcMode="linear" keyPoints="0;0;1;1" keyTimes="0;0.46;0.60;1"/>
                  <animate attributeName="opacity" values="0;1;1;0;0" keyTimes="0;0.46;0.59;0.62;1" dur="6s" repeatCount="indefinite"/>
                </circle>
                <circle className="ilx-dot sem" r="4.5" opacity="0">
                  <animateMotion path="M300 247 C330 247 350 230 380 230" dur="6s" repeatCount="indefinite" calcMode="linear" keyPoints="0;0;1;1" keyTimes="0;0.78;0.92;1"/>
                  <animate attributeName="opacity" values="0;1;1;0;0" keyTimes="0;0.78;0.91;0.94;1" dur="6s" repeatCount="indefinite"/>
                </circle>
                <rect className="ilx-flash lex" x="380" y="52" width="164" height="36" rx="8" opacity="0"><animate attributeName="opacity" values="0;0;.22;0;0" keyTimes="0;0.28;0.33;0.44;1" dur="6s" repeatCount="indefinite"/></rect>
                <rect className="ilx-flash str" x="380" y="132" width="164" height="36" rx="8" opacity="0"><animate attributeName="opacity" values="0;0;.22;0;0" keyTimes="0;0.60;0.65;0.76;1" dur="6s" repeatCount="indefinite"/></rect>
                <rect className="ilx-flash sem" x="380" y="212" width="164" height="36" rx="8" opacity="0"><animate attributeName="opacity" values="0;0;.22;0;0" keyTimes="0;0.92;0.96;0.995;1" dur="6s" repeatCount="indefinite"/></rect>
              </g>
            </svg>
          </div>
          <div className="live-copy">
            <div className="live-copy-set" key={copySet}>
              <span className="section-eyebrow">{t(liveCopySets[copySet].eyebrow)}</span>
              <h2>{t(liveCopySets[copySet].title)}</h2>
              <p className="section-lead">{t(liveCopySets[copySet].lead)}</p>
            </div>
            <div className="copy-dots">{liveCopySets.map((s,i)=><button key={i} type="button" className={i===copySet?'active':''} aria-label={t('Show copy set {n}',{n:i+1})} onClick={()=>setCopySet(i)}/>)}</div>
          </div>
        </div>
      </section>
      <section className="landing-screen" id="retrieval-screen">
        <div className="landing-features">
          <span className="section-eyebrow">{t('Retrieval system')}</span>
          <h2>{t('Three retrieval paths, one answer')}</h2>
          <p className="section-lead">{t('Every query runs three independent recalls in parallel; a reranker then cuts the candidates down to the fragments that actually answer the question.')}</p>
          <div className="landing-facts">
            <div className="fact path-lex"><strong>3</strong><small>{t('Retrieval paths fused')}</small></div>
            <div className="fact path-str"><strong>8</strong><small>{t('Languages with AST chunking')}</small></div>
            <div className="fact path-sem"><strong>1</strong><small>{t('Semantic understanding match')}</small></div>
          </div>
          <div className="landing-bento">
            <div className="bento-cell"><h3 className="path-lex"><Search size={14}/>{t('Lexical')}</h3><p>{t('BM25 over split identifiers and prefix tokens catches exact functions and variables.')}</p></div>
            <div className="bento-cell bento-main"><h3><BarChart2 size={14}/>{t('Rerank')}</h3><p>{t('Reciprocal-rank fusion merges all three signals, and a reranker keeps only the chunks that answer the question.')}</p></div>
            <div className="bento-cell"><h3 className="path-str"><Box size={14}/>{t('Structural')}</h3><p>{t('File paths and symbols score directly, so declarations surface first.')}</p></div>
            <div className="bento-cell"><h3 className="path-sem"><Database size={14}/>{t('Semantic')}</h3><p>{t('Dense vectors bridge natural language and code, across human languages.')}</p></div>
          </div>
        </div>
      </section>
      <section className="landing-screen" id="enhance-screen">
        <div className="landing-enhance">
          <div className="enhance-head">
            <span className="section-eyebrow">{t('Prompt enhancement')}</span>
            <h2>{t('From a vague question to a precise prompt')}</h2>
            <p className="section-lead">{t('The enhancer rewrites your question with codebase context retrieved from your latest checkpoint — before the model answers, the right files are already in view.')}</p>
          </div>
          <div className="enhance-panel" aria-hidden="true">
            <div className="enh-card">
              <span className="enh-label">{t('Your question')}</span>
              <p className="enh-question">{t('fix the login failure')}</p>
            </div>
            <div className="enh-arrow"><ArrowRight size={16}/></div>
            <div className="enh-card enh-after">
              <span className="enh-label">{t('Enhanced prompt')}</span>
              <div className="enh-output">
                <span className="enh-line">{t('Please help me fix the login failure.')}</span>{' '}
                <span className="enh-line">{t('Start with the auth middleware in internal/auth/middleware.go (lines 12–48, token validation) and the login flow in internal/auth/service.go (lines 90–121, credential verification and session issuance).')}</span>{' '}
                <span className="enh-line">{t('The login form lives in ui/src/pages/Login.tsx (lines 30–77) — locate where the failure occurs and propose a fix.')}<i className="enh-caret"/></span>
              </div>
            </div>
          </div>
        </div>
      </section>
      <section className="landing-screen" id="cost-screen">
        <div className="landing-adv">
          <div className="adv-intro">
            <span className="section-eyebrow">{t('Cost structure')}</span>
            <h2>{t('Cut the cost of context')}</h2>
            <p>{t('Precise fragments instead of whole files, metered usage instead of seats — context stops being the hidden bill of AI coding.')}</p>
          </div>
          <div className="cost-compare">
            <div className="cost-col">
              <h3>{t('Before')}</h3>
              <p className="cost-caption">{t('Whole-file context')}</p>
              <div className="cost-bar"><div className="cost-bar-track"><span style={{width:'92%'}}/></div><small>{t('Per-seat subscription')}</small></div>
              <div className="cost-bar"><div className="cost-bar-track"><span style={{width:'80%'}}/></div><small>{t('Re-indexing for every teammate')}</small></div>
              <div className="cost-bar"><div className="cost-bar-track"><span style={{width:'100%'}}/></div><small>{t('Whole files into the model')}</small></div>
            </div>
            <div className="cost-col cost-after">
              <h3>{t('After')}</h3>
              <p className="cost-caption">{t('Selected chunks only')}</p>
              <div className="cost-bar"><div className="cost-bar-track"><span style={{width:'12%'}}/></div><small>{t('Zero seat fees')}</small></div>
              <div className="cost-bar"><div className="cost-bar-track"><span style={{width:'28%'}}/></div><small>{t('One shared index')}</small></div>
              <div className="cost-bar"><div className="cost-bar-track"><span style={{width:'22%'}}/></div><small>{t('Only selected chunks')}</small></div>
            </div>
          </div>
        </div>
        <div className="adv-list">
          <div className="adv-row"><h3>{t('Lower model bills')}</h3><p>{t('Only fragments that answer the question reach the model, so token spend drops sharply.')}</p></div>
          <div className="adv-row"><h3>{t('Indexed once, shared by the team')}</h3><p>{t('A project is indexed once and every teammate searches the same index — no repeated setup, no duplicated cost.')}</p></div>
          <div className="adv-row"><h3>{t('Zero setup')}</h3><p>{t('No import, no configuration — the first search from your editor indexes the project automatically.')}</p></div>
          <div className="adv-row"><h3>{t('Always up to date')}</h3><p>{t('Edits are indexed as you work, so results always reflect your latest code.')}</p></div>
        </div>
      </section>
      <section className="landing-screen" id="mcp-screen">
        <div className="landing-mcp">
          <div className="mcp-copy">
            <span className="section-eyebrow">{t('Getting started')}</span>
            <h2>{t('MCP client configuration')}</h2>
            <p>{t('Paste into Cursor or any MCP client. The first search uploads and indexes the project.')}</p>
            <div className="steps-grid">
              <div className="step"><span className="step-index">01</span><h3>{t('Generate a token')}</h3><p>{t('Create your personal BCE token in account settings.')}</p></div>
              <div className="step"><span className="step-index">02</span><h3>{t('Configure your MCP client')}</h3><p>{t('Point bce-tool in Cursor or any MCP client at your BCE service.')}</p></div>
              <div className="step"><span className="step-index">03</span><h3>{t('Search from your editor')}</h3><p>{t('The first search uploads and indexes the project automatically.')}</p></div>
            </div>
            <p className="install-note">{t('No MCP? Install BCE as an')} <button type="button" className="inline-link" onClick={()=>{location.hash='skill'}}>{t('Agent Skill')}</button> {t('instead.')}</p>
          </div>
          <McpConfigPanel/>
        </div>
        <div className="tools-strip">
          <span className="tools-label">{t('Works with your agent tools')}</span>
          <div className="tools-list">
            <span className="tool"><CursorMark/>Cursor</span>
            <span className="tool"><ClaudeMark/>Claude Code</span>
            <span className="tool"><CodexMark/>Codex</span>
            <span className="tool"><VSCodeMark/>VS Code</span>
            <span className="tool"><ZedMark/>Zed</span>
          </div>
        </div>
      </section>
      <section className="landing-screen screen-end">
        <div className="landing-end">
          <BrandLogo size={36}/>
          <h2>{t('Less context. Sharper answers.')}</h2>
          <p className="end-lead">{t('Hosted in our cloud, ready in minutes — or privately deployed when code must stay in.')}</p>
          <Button auto type="secondary" className="brand-btn" onClick={onSignIn}>{t('Get started')}</Button>
        </div>
        <div className="end-index">
          <button type="button" className="end-cell" onClick={()=>scrollToScreen('indexing-screen')}><span className="funnel-index">01</span><strong>{t('Live indexing')}</strong><small>{t('Always up to date')}</small><ArrowUpRight size={14}/></button>
          <button type="button" className="end-cell" onClick={()=>scrollToScreen('retrieval-screen')}><span className="funnel-index">02</span><strong>{t('Retrieval system')}</strong><small>{t('Three retrieval paths, one answer')}</small><ArrowUpRight size={14}/></button>
          <button type="button" className="end-cell" onClick={()=>scrollToScreen('enhance-screen')}><span className="funnel-index">03</span><strong>{t('Prompt enhancement')}</strong><small>{t('From a vague question to a precise prompt')}</small><ArrowUpRight size={14}/></button>
          <button type="button" className="end-cell" onClick={()=>scrollToScreen('cost-screen')}><span className="funnel-index">04</span><strong>{t('Cost structure')}</strong><small>{t('Pay per token, not per seat')}</small><ArrowUpRight size={14}/></button>
          <button type="button" className="end-cell" onClick={()=>scrollToScreen('mcp-screen')}><span className="funnel-index">05</span><strong>{t('Getting started')}</strong><small>{t('Connect in three steps')}</small><ArrowUpRight size={14}/></button>
        </div>
        <SiteFooter/>
      </section>
    </main>
    <button type="button" className={`back-top ${showTop?'show':''}`} aria-label={t('Back to top')} onClick={scrollToTop}><ArrowUp size={16}/></button>
  </div>
}

