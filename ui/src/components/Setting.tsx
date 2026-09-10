export function Setting({label,children}:{label:string;children:React.ReactNode}){
  return <label className="setting-field"><span>{label}</span>{children}</label>
}
