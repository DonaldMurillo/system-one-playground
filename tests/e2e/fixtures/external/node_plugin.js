const readline = require('node:readline')

const lines = readline.createInterface({ input: process.stdin, crlfDelay: Infinity })
lines.on('line', line => {
  const request = JSON.parse(line)
  const { method, params } = request
  let result
  if (method === 'initialize') {
    result = {
      protocol: params.protocol,
      module: params.module,
      version: params.version,
      definitionDigest: params.definitionDigest,
    }
  } else if (method === 'invoke') {
    result = { value: String(params.arguments.value).toUpperCase() }
  } else if (method === 'shutdown') {
    result = null
  } else return
  process.stdout.write(`${JSON.stringify({ jsonrpc: '2.0', id: request.id, result })}\n`)
  if (method === 'shutdown') process.exit(0)
})
