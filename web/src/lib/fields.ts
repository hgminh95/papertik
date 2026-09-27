// Visual identity for OpenAlex's computer-science subfields: a short name, an icon, and for the
// three largest fields (~3/4 of papers) a colour. Colour follows the field, never its position on
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

const FIELDS: Record<string, FieldStyle> = {
  'Artificial Intelligence': {
    short: 'AI',
    color: '#3987e5',
    icon: 'M12 3l1.8 4.7L18.5 9.5l-4.7 1.8L12 16l-1.8-4.7L5.5 9.5l4.7-1.8zM18.5 15l.8 2.2 2.2.8-2.2.8-.8 2.2-.8-2.2-2.2-.8 2.2-.8z',
  },
  'Computer Vision and Pattern Recognition': {
    short: 'Computer Vision',
    color: '#d95926',
    icon: 'M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12zm10 3a3 3 0 1 0 0-6 3 3 0 0 0 0 6z',
  },
  'Computer Networks and Communications': {
    short: 'Networks',
    color: '#199e70',
    icon: 'M12 5a2 2 0 1 0 0-.01M5 19a2 2 0 1 0 0-.01M19 19a2 2 0 1 0 0-.01M12 7v4m0 0-6 6m6-6 6 6',
  },
  'Computational Theory and Mathematics': {
    short: 'Theory',
    color: NEUTRAL,
    icon: 'M18 5H6l6 7-6 7h12',
  },
  'Information Systems': {
    short: 'Information Systems',
    color: NEUTRAL,
    icon: 'M4 6c0-1.7 3.6-3 8-3s8 1.3 8 3-3.6 3-8 3-8-1.3-8-3zm0 0v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3',
  },
  'Signal Processing': {
    short: 'Signal Processing',
    color: NEUTRAL,
    icon: 'M2 12h3l2-6 4 12 4-14 3 8h4',
  },
  'Hardware and Architecture': {
    short: 'Hardware',
    color: NEUTRAL,
    icon: 'M7 7h10v10H7zM10 3v4m4-4v4m-4 10v4m4-4v4M3 10h4m-4 4h4m10-4h4m-4 4h4',
  },
  'Human-Computer Interaction': {
    short: 'HCI',
    color: NEUTRAL,
    icon: 'M9 11V5a1.5 1.5 0 0 1 3 0v5m0-1a1.5 1.5 0 0 1 3 0v2m0-1a1.5 1.5 0 0 1 3 0v4a6 6 0 0 1-6 6h-1a6 6 0 0 1-5-2.7L3.5 14a1.5 1.5 0 0 1 2.5-1.6L9 16',
  },
  'Computer Graphics and Computer-Aided Design': {
    short: 'Graphics',
    color: NEUTRAL,
    icon: 'M12 2l9 5v10l-9 5-9-5V7zm0 0v10m9-5-9 5-9-5',
  },
  Software: {
    short: 'Software',
    color: NEUTRAL,
    icon: 'M8 7l-5 5 5 5m8-10 5 5-5 5M14 4l-4 16',
  },
  'Computer Science Applications': {
    short: 'Applications',
    color: NEUTRAL,
    icon: 'M4 4h7v7H4zm9 0h7v7h-7zM4 13h7v7H4zm9 0h7v7h-7z',
  },
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
