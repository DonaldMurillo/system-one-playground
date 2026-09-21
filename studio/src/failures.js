export function failureOutput(error) {
  if (!error || typeof error !== 'object') return String(error || 'run failed')
  const headline = [error.kind || 'runtime', error.message || 'run failed'].join(': ')
  const details = {...error}
  delete details.kind
  delete details.message
  const frames = Array.isArray(details.frames) ? details.frames : []
  delete details.frames
  const lines = [headline]
  if (Object.keys(details).length) lines.push(JSON.stringify(details, null, 2))
  if (frames.length) {
    lines.push('Frames:')
    for (const [index, frame] of frames.entries()) {
      const label = frame.action || frame.name || frame.function || `frame ${index + 1}`
      const location = [frame.path || frame.file, frame.line].filter(v => v !== undefined && v !== '').join(':')
      lines.push(`  ${index + 1}. ${label}${location ? ` (${location})` : ''}`)
      const extra = {...frame}
      delete extra.action
      delete extra.name
      delete extra.function
      delete extra.path
      delete extra.file
      delete extra.line
      if (Object.keys(extra).length) lines.push(indent(JSON.stringify(extra, null, 2), '     '))
    }
  }
  return lines.join('\n')
}

export function actionFailureContracts(actions) {
  return (Array.isArray(actions) ? actions : [])
    .filter(action => Array.isArray(action?.possibleFailures) && action.possibleFailures.length)
    .map(action => ({name: action.name, failures: action.possibleFailures.slice()}))
}

function indent(value, prefix) {
  return value.split('\n').map(line => prefix + line).join('\n')
}
