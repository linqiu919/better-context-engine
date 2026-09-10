export function Panel({title,meta,children}:{title:string;meta?:string;children:React.ReactNode}){
  return <section className="panel">
    <div className="panel-head"><h2>{title}</h2>{meta&&<span>{meta}</span>}</div>
    <div className="panel-body">{children}</div>
  </section>
}
