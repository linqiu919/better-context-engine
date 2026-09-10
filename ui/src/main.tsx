import React from 'react'
import ReactDOM from 'react-dom'
import { CssBaseline, GeistProvider } from '@geist-ui/core'
import App from './App'
import './styles.css'
import { LocaleProvider } from './i18n'

ReactDOM.render(
  <React.StrictMode>
    <GeistProvider>
      <CssBaseline />
      <LocaleProvider><App /></LocaleProvider>
    </GeistProvider>
  </React.StrictMode>, document.getElementById('root'),
)
