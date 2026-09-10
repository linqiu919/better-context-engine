import { useEffect, useState } from 'react'
import { Button, Input, Select } from '@geist-ui/core'
import { api, jsonBody } from '../api'
import type { AuthConfig } from '../types'
import { useI18n } from '../i18n'
import type { AuthState } from '../nav'
import { LoginShell } from './LoginShell'
import { Turnstile } from './Turnstile'

export function Register({onLogin,onBack,cfg,dark,toggleTheme}:{onLogin:(a:AuthState)=>void;onBack:()=>void;cfg:AuthConfig|null;dark:boolean;toggleTheme:()=>void}){
  const {t}=useI18n()
  // 邮箱域名白名单由后端 authConfig 下发；输入拆成「前缀 + 域名下拉」，
  // 粘贴完整邮箱时自动按 @ 拆分（域名在白名单内则同步选中）。
  const domains=cfg?.email_domains?.length?cfg.email_domains:['gmail.com','163.com','qq.com']
  const [emailLocal,setEmailLocal]=useState(''),[emailDomain,setEmailDomain]=useState(domains[0])
  const email=emailLocal.trim()?`${emailLocal.trim()}@${emailDomain}`:''
  const onEmailLocal=(v:string)=>{const i=v.indexOf('@');if(i<0){setEmailLocal(v);return}const dom=v.slice(i+1);setEmailLocal(v.slice(0,i));if(domains.includes(dom))setEmailDomain(dom)}
  const [username,setUsername]=useState(''),[code,setCode]=useState('')
  const [password,setPassword]=useState(''),[confirm,setConfirm]=useState('')
  const [error,setError]=useState(''),[notice,setNotice]=useState(''),[busy,setBusy]=useState(false)
  const [sending,setSending]=useState(false),[countdown,setCountdown]=useState(0)
  const [tsToken,setTsToken]=useState(''),[tsReset,setTsReset]=useState(0)
  useEffect(()=>{if(countdown<=0)return;const timer=setTimeout(()=>setCountdown(v=>v-1),1000);return()=>clearTimeout(timer)},[countdown])
  // Turnstile tokens are single-use: both sending a code and submitting the
  // form consume one, so request a fresh token after each server call.
  const consumeToken=()=>{setTsToken('');setTsReset(v=>v+1)}
  const sendCode=async()=>{
    setSending(true);setError('');setNotice('')
    try{await api('/api/v1/auth/register/send-code',{method:'POST',...jsonBody({email,turnstile_token:tsToken})});setNotice('Code sent');setCountdown(60)}
    catch(e){setError((e as Error).message)}
    finally{consumeToken();setSending(false)}
  }
  const submit=async(e:React.FormEvent)=>{
    e.preventDefault()
    if(password!==confirm){setError('Passwords do not match');return}
    setBusy(true);setError('');setNotice('')
    try{onLogin(await api<AuthState>('/api/v1/auth/register',{method:'POST',...jsonBody({username,email,code,password,confirm_password:confirm,turnstile_token:tsToken})}))}
    catch(e){setError((e as Error).message);consumeToken()}
    finally{setBusy(false)}
  }
  return <LoginShell onBack={onBack} dark={dark} toggleTheme={toggleTheme} onSubmit={submit}>
    <span className="login-fields-title">{t('Create account')}</span>
    <p className="login-hint">{t('Create your BCE account with a verified email.')}</p>
    <Input crossOrigin="" width="100%" autoComplete="username" value={username} onChange={e=>setUsername(e.target.value)}>{t('Username')}</Input>
    <div className="email-row">
      <Input crossOrigin="" width="100%" autoComplete="off" value={emailLocal} onChange={e=>onEmailLocal(e.target.value)}>{t('Email')}</Input>
      <span className="email-at" aria-hidden="true">@</span>
      <Select className="email-domain" value={emailDomain} onChange={v=>setEmailDomain(v as string)} aria-label={t('Email provider')}>
        {domains.map(d=><Select.Option key={d} value={d}>{d}</Select.Option>)}
      </Select>
    </div>
    <div className="code-row">
      <Input crossOrigin="" width="100%" autoComplete="one-time-code" value={code} onChange={e=>setCode(e.target.value)}>{t('Verification code')}</Input>
      <Button auto className="code-btn" onClick={sendCode} disabled={sending||countdown>0||!email||(!!cfg?.turnstile_site_key&&!tsToken)} loading={sending}>{countdown>0?`${countdown}s`:t('Send code')}</Button>
    </div>
    <Input.Password crossOrigin="" width="100%" autoComplete="new-password" value={password} onChange={e=>setPassword(e.target.value)}>{t('Password')}</Input.Password>
    <Input.Password crossOrigin="" width="100%" autoComplete="new-password" value={confirm} onChange={e=>setConfirm(e.target.value)}>{t('Confirm password')}</Input.Password>
    {cfg?.turnstile_site_key&&<Turnstile siteKey={cfg.turnstile_site_key} dark={dark} onToken={setTsToken} resetSignal={tsReset}/>}
    {error&&<p className="form-error" role="alert">{t(error)}</p>}
    {notice&&!error&&<p className="form-notice" role="status">{t(notice)}</p>}
    <Button htmlType="submit" type="secondary" className="brand-btn" width="100%" loading={busy} disabled={!!cfg?.turnstile_site_key&&!tsToken}>{t('Create account')}</Button>
    <p className="login-alt">{t('Already have an account?')} <button type="button" className="login-link" onClick={onBack}>{t('Sign in')}</button></p>
  </LoginShell>
}
