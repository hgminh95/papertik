import { mount } from 'svelte'
import App from './App.svelte'
import './app.css'

// Server-rendered paper pages (/p/W123) carry a readable copy for crawlers; the app replaces it.
document.getElementById('seo')?.remove()

export default mount(App, { target: document.getElementById('app')! })
