export function PageHeader({title,description,actions}:{title:string;description:string;actions?:React.ReactNode}){
  return <div className="page-header">
    <div className="page-header-copy"><h1>{title}</h1><p>{description}</p></div>
    {actions&&<div className="page-actions">{actions}</div>}
  </div>
}
