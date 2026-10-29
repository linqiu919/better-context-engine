export function Panel({title,meta,actions,children}:{title:string;meta?:string;actions?:React.ReactNode;children:React.ReactNode}){
  return <section className="panel">
    <div className="panel-head"><h2>{title}</h2>{meta&&<span>{meta}</span>}{actions}</div>
    <div className="panel-body">{children}</div>
  </section>
}
