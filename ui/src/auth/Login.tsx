import { useEffect, useState } from 'react'
import { Button, Input } from '@geist-ui/core'
import { api, jsonBody } from '../api'
import type { AuthConfig } from '../types'
import { useI18n } from '../i18n'
import type { AuthState } from '../nav'
import { LoginShell } from './LoginShell'
import { Turnstile } from './Turnstile'

export function Login({onLogin,onBack,onRegister,cfg,dark,toggleTheme}:{onLogin:(a:AuthState)=>void;onBack:()=>void;onRegister:()=>void;cfg:AuthConfig|null;dark:boolean;toggleTheme:()=>void}){
  const {t}=useI18n()
  const [username,setUsername]=useState(''),[password,setPassword]=useState(''),[error,setError]=useState(''),[busy,setBusy]=useState(false)
  const [tsToken,setTsToken]=useState(''),[tsReset,setTsReset]=useState(0)
  // A failed OAuth round trip lands back here with the reason in the query
  // string; surface it once and clean the URL.
  useEffect(()=>{const reason=new URLSearchParams(location.search).get('oauth_error');if(reason){setError(reason);history.replaceState(null,'',location.pathname+location.hash)}},[])
  const submit=async(e:React.FormEvent)=>{
    e.preventDefault();setBusy(true);setError('')
    try{onLogin(await api<AuthState>('/api/v1/auth/login',{method:'POST',...jsonBody({username,password,turnstile_token:tsToken})}))}
    catch(e){setError((e as Error).message);setTsToken('');setTsReset(v=>v+1)}
    finally{setBusy(false)}
  }
  return <LoginShell onBack={onBack} dark={dark} toggleTheme={toggleTheme} onSubmit={submit}>
    <span className="login-fields-title">{t('Sign in')}</span>
    <p className="login-hint">{t('Sign in with your console account.')}</p>
    <Input crossOrigin="" width="100%" autoComplete="username" value={username} onChange={e=>setUsername(e.target.value)}>{t('Username')}</Input>
    <Input.Password crossOrigin="" width="100%" autoComplete="current-password" value={password} onChange={e=>setPassword(e.target.value)}>{t('Password')}</Input.Password>
    {cfg?.turnstile_site_key&&<Turnstile siteKey={cfg.turnstile_site_key} dark={dark} onToken={setTsToken} resetSignal={tsReset}/>}
    {error&&<p className="form-error" role="alert">{t(error)}</p>}
    <Button htmlType="submit" type="secondary" className="brand-btn" width="100%" loading={busy} disabled={!!cfg?.turnstile_site_key&&!tsToken}>{t('Sign in')}</Button>
    {cfg?.linuxdo_enabled&&<>
      <div className="login-divider"><span>{t('or')}</span></div>
      <a className="oauth-btn" href="/api/v1/auth/linuxdo"><img className="oauth-icon" src="/linuxdo.svg" alt="" aria-hidden="true"/>{t('Sign in with LinuxDo')}</a>
    </>}
    {cfg?.registration_enabled&&<p className="login-alt">{t('No account yet?')} <button type="button" className="login-link" onClick={onRegister}>{t('Create account')}</button></p>}
  </LoginShell>
}
