import { Button } from '@geist-ui/core'
import { useI18n } from '../i18n'

// LoadError is the failed-fetch state: it names the failure and offers a
// retry, so an error never reads as an empty list or an endless spinner.
export function LoadError({error,onRetry}:{error:string;onRetry:()=>void}){
  const {t}=useI18n()
  return <div className="load-error" role="alert"><p className="form-error">{t('Failed to load')}: {t(error)}</p><Button auto scale={.6} onClick={onRetry}>{t('Retry')}</Button></div>
}
