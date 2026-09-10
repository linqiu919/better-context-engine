import { useState } from 'react'
import { Button } from '@geist-ui/core'
import { ArrowDown } from '@geist-ui/icons'
import { useI18n } from '../i18n'
import { prefersReducedMotion } from '../lib/motion'
import { LangThemeSwitch } from '../components/LangThemeSwitch'
import { McpConfigPanel } from './McpConfigPanel'
import { PublicNav, SiteFooter } from './PublicChrome'

// Public bce-tool page ('#mcp'): the companion MCP server's story — its two
// tools, the engineering traits, and install/config. Shares the landing's
// glass nav, paper tokens and serif display headings.
export function McpPage({dark,toggleTheme}:{dark:boolean;toggleTheme:()=>void}){
  const {t}=useI18n()
  const [scrolled,setScrolled]=useState(false)
  const goHome=()=>{location.hash=''}
  const goLogin=()=>{location.hash='login'}
  const goSkill=()=>{location.hash='skill'}
  const scrollToInstall=()=>{document.getElementById('mcp-install')?.scrollIntoView({behavior:prefersReducedMotion()?'auto':'smooth'})}
  const features:[string,string][]=[
    ['Incremental indexing','Unchanged files are skipped via mtime caching; only new and modified content is uploaded.'],
    ['50+ languages','Source files in over fifty languages and file types are recognized and indexed.'],
    ['Multi-encoding','UTF-8, GBK, GB18030 and Windows-1252 files are read correctly.'],
    ['Adaptive uploads','An AIMD algorithm tunes concurrency and timeout from runtime metrics.'],
    ['Parallel scanning','Multi-threaded file scanning keeps large projects fast.'],
    ['Smart exclusions','Respects .gitignore, .bceignore and common ignore patterns.'],
  ]
  return <div className="mcp-page" onScroll={e=>setScrolled(e.currentTarget.scrollTop>24)}>
    <PublicNav scrolled={scrolled} onBrand={goHome} brandLabel={t('Back to home')}>
      <LangThemeSwitch dark={dark} toggleTheme={toggleTheme}/>
      <Button auto scale={0.75} type="secondary" onClick={goLogin}>{t('Get started')}</Button>
    </PublicNav>
    <main className="mcp-main">
      <section className="mcp-hero">
        <span className="hero-eyebrow"><i/>{t('MCP server · Written in Rust')}</span>
        <h1>bce-tool</h1>
        <p>{t('The purpose-built MCP client for the BCE engine.')}</p>
        <div className="landing-cta"><Button auto type="secondary" className="brand-btn" onClick={goLogin}>{t('Get started')}</Button><button type="button" className="ghost-link" onClick={scrollToInstall}>{t('View configuration')}<ArrowDown size={13}/></button></div>
        <div className="spec-strip" aria-hidden="true">
          <span><em>PROTOCOL</em>MCP · stdio</span>
          <span><em>RUNTIME</em>Rust</span>
          <span><em>PACKAGE</em>npx</span>
          <span><em>PLATFORMS</em>Win · macOS · Linux</span>
        </div>
      </section>
      <section className="mcp-section">
        <span className="section-eyebrow">{t('How it works')}</span>
        <h2>{t('Between your editor and the engine')}</h2>
        <p className="section-lead">{t('The BCE engine’s own MCP client: fluent in checkpoints, incremental uploads and the retrieval protocol — ready out of the box.')}</p>
        <div className="flow-diagram" aria-hidden="true">
          <div className="flow-node"><strong>{t('AI coding tool')}</strong><small>Cursor · Claude Code · Codex · Zed</small></div>
          <div className="flow-link"><span>MCP · stdio</span><i/></div>
          <div className="flow-node flow-node-core"><strong>bce-tool</strong><small>{t('purpose-built for BCE')}</small></div>
          <div className="flow-link"><span>HTTPS</span><i/></div>
          <div className="flow-node"><strong>{t('BCE service')}</strong><small>{t('retrieval & enhancement')}</small></div>
        </div>
      </section>
      <section className="mcp-section">
        <span className="section-eyebrow">{t('MCP tools')}</span>
        <h2>{t('Retrieval and enhancement, each in its place')}</h2>
        <article className="tool-doc">
          <header><code>search_context</code><p>{t('Search the codebase with natural language — ask where something lives or how it works.')}</p></header>
          <div className="param-table">
            <div className="param-row param-head"><span>{t('Parameter')}</span><span>{t('Type')}</span><span>{t('Required')}</span></div>
            <div className="param-row"><span><code>project_root_path</code></span><span>string</span><span>{t('Yes')}</span></div>
            <div className="param-row"><span><code>query</code></span><span>string</span><span>{t('Yes')}</span></div>
          </div>
          <div className="tool-sample">{t('"Where is user authentication handled?"')}</div>
        </article>
        <article className="tool-doc">
          <header><code>enhance_prompt</code><p>{t('Rewrite a rough prompt with retrieved codebase context and recent conversation history, so the model starts with the right files in view.')}</p></header>
          <div className="param-table">
            <div className="param-row param-head"><span>{t('Parameter')}</span><span>{t('Type')}</span><span>{t('Required')}</span></div>
            <div className="param-row"><span><code>prompt</code></span><span>string</span><span>{t('Yes')}</span></div>
            <div className="param-row"><span><code>conversation_history</code></span><span>string</span><span>{t('Yes')}</span></div>
            <div className="param-row"><span><code>project_root_path</code></span><span>string</span><span>{t('No')}</span></div>
          </div>
          <div className="tool-sample">{t('"fix the login failure" → a full prompt with the right files and line numbers')}</div>
        </article>
      </section>
      <section className="mcp-section">
        <span className="section-eyebrow">{t('Engineering')}</span>
        <h2>{t('Built for large codebases')}</h2>
        <div className="feat-list">
          {features.map(([title,desc])=><div className="feat-item" key={title}><strong>{t(title)}</strong><p>{t(desc)}</p></div>)}
        </div>
      </section>
      <section className="mcp-section" id="mcp-install">
        <span className="section-eyebrow">{t('Install')}</span>
        <h2>{t('One command to run')}</h2>
        <div className="term-panel">
          <div className="win-chrome"><i/><i/><i/><span>terminal</span></div>
          <pre><code><span className="term-prompt">$ npx bce-tool --base-url https://bce.wxnext.top --token bce_...</span>{'\n'}<span className="term-ok">✓</span> <span className="term-out">project indexed</span>{'\n'}<span className="term-ok">✓</span> <span className="term-out">watching for changes (mtime cache)</span>{'\n'}<span className="term-ok">✓</span> <span className="term-out">MCP server ready · stdio</span></code></pre>
        </div>
        <McpConfigPanel/>
        <p className="install-note">{t('Windows, macOS and Linux, x64 and ARM64. Also works with Codex CLI, Claude Desktop, Claude Code and OpenCode.')}</p>
        <p className="install-note">{t('No MCP? Install BCE as an')} <button type="button" className="inline-link" onClick={goSkill}>{t('Agent Skill')}</button> {t('instead.')}</p>
      </section>
      <section className="mcp-section mcp-end">
        <h2>{t('Let your tools read the codebase.')}</h2>
        <Button auto type="secondary" className="brand-btn" onClick={goLogin}>{t('Get started')}</Button>
      </section>
      <SiteFooter/>
    </main>
  </div>
}

