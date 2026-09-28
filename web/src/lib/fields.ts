// Visual identity for PaperTik's categories (ingest/taxonomy.json): a short name, an icon, and
// for three of the largest categories a colour. Colour follows the field, never its position on
// screen. Only three hues: tiles put any two fields side by side, and only the first three slots
// of the dark categorical palette (blue, orange, aqua) stay distinguishable for every pair,
// including colour-blind viewers (checked with the dataviz palette validator). Every field has
// its own icon, so colour is never the only cue.

export interface FieldStyle {
  short: string
  color: string // CSS colour for the icon
  icon: string // SVG path data, 24x24, stroked
}

const NEUTRAL = 'var(--fg-soft)'

const I = {
  AI: 'M12 3l1.8 4.7L18.5 9.5l-4.7 1.8L12 16l-1.8-4.7L5.5 9.5l4.7-1.8zM18.5 15l.8 2.2 2.2.8-2.2.8-.8 2.2-.8-2.2-2.2-.8 2.2-.8z',
  NLP: 'M4 5h16v11H10l-5 4v-4H4zM8 9h8M8 12h5',
  CV: 'M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12zm10 3a3 3 0 1 0 0-6 3 3 0 0 0 0 6z',
  PL: 'M6 4h3l9 16M11.8 10.5 6 20',
  SEC: 'M7 11V8a5 5 0 0 1 10 0v3M6 11h12v10H6zM12 15v2',
  QC: 'M3 12c0-2.5 4-4.5 9-4.5s9 2 9 4.5-4 4.5-9 4.5-9-2-9-4.5zM12 3c2.5 0 4.5 4 4.5 9s-2 9-4.5 9-4.5-4-4.5-9 2-9 4.5-9zM12 12h.01',
  TH: 'M18 5H6l6 7-6 7h12',
  SYS: 'M4 4h16v6H4zM4 14h16v6H4zM8 7h.01M8 17h.01',
  NET: 'M12 5a2 2 0 1 0 0-.01M5 19a2 2 0 1 0 0-.01M19 19a2 2 0 1 0 0-.01M12 7v4m0 0-6 6m6-6 6 6',
  DB: 'M4 6c0-1.7 3.6-3 8-3s8 1.3 8 3-3.6 3-8 3-8-1.3-8-3zm0 0v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3',
  SE: 'M8 7l-5 5 5 5m8-10 5 5-5 5M14 4l-4 16',
  HCI: 'M9 11V5a1.5 1.5 0 0 1 3 0v5m0-1a1.5 1.5 0 0 1 3 0v2m0-1a1.5 1.5 0 0 1 3 0v4a6 6 0 0 1-6 6h-1a6 6 0 0 1-5-2.7L3.5 14a1.5 1.5 0 0 1 2.5-1.6L9 16',
  GR: 'M12 2l9 5v10l-9 5-9-5V7zm0 0v10m9-5-9 5-9-5',
  ROB: 'M6 8h12v10H6zM12 4v4M9.5 12h.01M14.5 12h.01M9 15h6M3 11v4M21 11v4',
  SIG: 'M2 12h3l2-6 4 12 4-14 3 8h4',
  BIO: 'M7 3c0 6 10 6 10 12s-10 6-10 6M17 3c0 6-10 6-10 12M8.5 7h7M8.5 17h7',
  SOC: 'M9 11a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM3 20a6 6 0 0 1 12 0M16 5.5a3 3 0 0 1 0 5.5M21 20a6 6 0 0 0-4-5.6',
  HW: 'M7 7h10v10H7zM10 3v4m4-4v4m-4 10v4m4-4v4M3 10h4m-4 4h4m10-4h4m-4 4h4',
  APP: 'M4 4h7v7H4zm9 0h7v7h-7zM4 13h7v7H4zm9 0h7v7h-7z',
}

const FIELDS: Record<string, FieldStyle> = {
  'Artificial Intelligence': { short: 'AI', color: '#3987e5', icon: I.AI },
  'Natural Language Processing': { short: 'NLP', color: NEUTRAL, icon: I.NLP },
  'Computer Vision': { short: 'Computer Vision', color: '#d95926', icon: I.CV },
  'Programming Languages': { short: 'Programming Languages', color: NEUTRAL, icon: I.PL },
  'Security & Cryptography': { short: 'Security', color: NEUTRAL, icon: I.SEC },
  'Quantum Computing': { short: 'Quantum', color: NEUTRAL, icon: I.QC },
  'Theory & Algorithms': { short: 'Theory', color: NEUTRAL, icon: I.TH },
  'Systems & Architecture': { short: 'Systems', color: NEUTRAL, icon: I.SYS },
  Networks: { short: 'Networks', color: '#199e70', icon: I.NET },
  'Databases & Information Retrieval': { short: 'Databases & IR', color: NEUTRAL, icon: I.DB },
  'Software Engineering': { short: 'Software Eng.', color: NEUTRAL, icon: I.SE },
  'Human-Computer Interaction': { short: 'HCI', color: NEUTRAL, icon: I.HCI },
  'Graphics & Visualization': { short: 'Graphics', color: NEUTRAL, icon: I.GR },
  Robotics: { short: 'Robotics', color: NEUTRAL, icon: I.ROB },
  'Signal Processing': { short: 'Signal Processing', color: NEUTRAL, icon: I.SIG },
  'Computational Biology & Health': { short: 'Bio & Health', color: NEUTRAL, icon: I.BIO },
  'Computing & Society': { short: 'Society', color: NEUTRAL, icon: I.SOC },
  'Other Computing': { short: 'Other', color: NEUTRAL, icon: I.APP },
  'Computer Vision and Pattern Recognition': { short: 'Computer Vision', color: '#d95926', icon: I.CV },
  'Computer Networks and Communications': { short: 'Networks', color: '#199e70', icon: I.NET },
  'Computational Theory and Mathematics': { short: 'Theory', color: NEUTRAL, icon: I.TH },
  'Information Systems': { short: 'Information Systems', color: NEUTRAL, icon: I.DB },
  'Hardware and Architecture': { short: 'Hardware', color: NEUTRAL, icon: I.HW },
  'Computer Graphics and Computer-Aided Design': { short: 'Graphics', color: NEUTRAL, icon: I.GR },
  Software: { short: 'Software', color: NEUTRAL, icon: I.SE },
  'Computer Science Applications': { short: 'Applications', color: NEUTRAL, icon: I.APP },
}

const OTHER: FieldStyle = {
  short: '',
  color: NEUTRAL,
  icon: 'M6 2h8l6 6v13a1 1 0 0 1-1 1H6a1 1 0 0 1-1-1V3a1 1 0 0 1 1-1zm7 1.5V9h5.5',
}

export function fieldStyle(field: string | undefined): FieldStyle {
  if (field && FIELDS[field]) return FIELDS[field]
  return { ...OTHER, short: field || 'Paper' }
}
