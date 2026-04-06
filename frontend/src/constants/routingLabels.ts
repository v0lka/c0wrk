// Human-readable value mappings for routing messages
export const domainLabels: Record<string, string> = {
  general: 'General',
  code: 'Code',
  research: 'Research',
  mixed: 'Mixed',
}

export const modeLabels: Record<string, string> = {
  direct: 'Direct',
  react: 'ReAct',
  plan_execute: 'Plan&Execute',
}

export const complexityStars: Record<string, string> = {
  '1': '★☆☆☆☆',
  '2': '★★☆☆☆',
  '3': '★★★☆☆',
  '4': '★★★★☆',
  '5': '★★★★★',
}
