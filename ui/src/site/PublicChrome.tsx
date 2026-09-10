import { BrandLogo } from '../BrandLogo'

// PublicNav is the floating glass top bar shared by the landing, bce-tool and
// sign-in/registration screens: brand button on the left (its action differs
// per screen), the caller's action buttons on the right.
export function PublicNav({scrolled,onBrand,brandLabel,children}:{scrolled:boolean;onBrand:()=>void;brandLabel:string;children:React.ReactNode}){
  return <header className={`landing-nav ${scrolled?'scrolled':''}`}>
    <button className="landing-brand landing-brand-btn" type="button" onClick={onBrand} aria-label={brandLabel}><BrandLogo size={22}/><div className="landing-brand-text"><strong>BCE</strong><small>BETTER CONTEXT ENGINE</small></div></button>
    <div className="landing-nav-actions">{children}</div>
  </header>
}

// SiteFooter closes the landing and bce-tool pages with the brand line.
export function SiteFooter(){
  return <footer className="landing-footer">
    <div className="landing-brand"><BrandLogo size={18}/><strong>BCE</strong></div>
    <small>BETTER CONTEXT ENGINE</small>
  </footer>
}
