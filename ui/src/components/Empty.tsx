import { Archive } from '@geist-ui/icons'

export function Empty({title,text}:{title:string;text:string}){
  return <div className="empty"><span><Archive size={22}/></span><strong>{title}</strong><p>{text}</p></div>
}
