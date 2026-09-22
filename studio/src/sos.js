// Immediate offline lexical colors; semantic meaning comes from the shared SOS LSP.

export const SENTENCE_STARTERS = [
  'evaluate', 'criterion', 'package', 'import', 'export', 'define', 'failure', 'argument', 'option', 'switch', 'to', 'call', 'returning', 'may', 'finish', 'fail', 'recover', 'capture', 'return', 'while', 'repeat', 'set', 'emit', 'judge', 'score', 'command', 'describe', 'expect', 'remember', 'find', 'read', 'require',
  'keep', 'sort', 'group', 'create', 'make', 'assign', 'when', 'take',
  'classify', 'append', 'save', 'show', 'for', 'map', 'stop', 'print',
  'stream', 'streaming', 'send', 'close', 'collect', 'get', 'post', 'listen', 'respond'
]

export const CONNECTORS = [
  'each', 'in', 'as', 'into', 'on', 'called', 'with', 'where', 'by', 'from',
  'under', 'named', 'matching', 'if', 'missing', 'numbered', 'otherwise',
  'and', 'or', 'not', 'of', 'count', 'first', 'last', 'items', 'default',
  'choices', 'is', 'ascending', 'descending', 'existing', 'off', 'at', 'most', 'running', 'collecting', 'failures', 'rethrow', 'pass', 'using', 'reading', 'HTTP', 'status', 'headers', 'body', 'redirects', 'interfaces', 'port', 'deadline', 'accepting'
]

export const OPERATORS = ['and', 'contains', 'is', 'minus', 'not', 'or', 'plus', 'times']


export const TYPE_WORDS = [
  'text', 'timestamp', 'duration', 'folder', 'list', 'json', 'table',
  'number', 'integer', 'boolean', 'switch', 'JSON'
]

export const JUDGMENT_WORDS = [
  'jev', 'classify', 'judge', 'confidence', 'value', 'p_yes', 'noul', 'choice', 'score'
]

export const LITERALS = ['true', 'false', 'null', 'now']

export const SOS_KEYWORDS = [
  ...new Set([...SENTENCE_STARTERS, ...CONNECTORS, ...TYPE_WORDS, ...JUDGMENT_WORDS, ...LITERALS])
]

export function registerSOSLanguage(monaco) {
  if (monaco.languages.getLanguages().some((l) => l.id === 'sos')) return

  monaco.languages.register({ id: 'sos', extensions: ['.sos'] })

  monaco.languages.setMonarchTokensProvider('sos', {
    defaultToken: '',
    tokenPostfix: '.sos',
    starters: SENTENCE_STARTERS,
    connectors: CONNECTORS,
    operators: OPERATORS,
    types: TYPE_WORDS,
    judgment: JUDGMENT_WORDS,
    literals: LITERALS,
    tokenizer: {
      root: [
        [/#.*$/, 'comment'],
        [/"/, { token: 'string', next: '@string' }],
        [/\d+(\.\d+)?([hms]|ms)?\b/, 'number'],
        [/[a-zA-Z_][a-zA-Z0-9_-]*/, {
          cases: {
            '@starters': 'keyword',
            '@operators': 'operator',
            '@connectors': 'connector',
            '@judgment': 'judgment',
            '@types': 'type',
            '@literals': 'literal',
            '@default': 'identifier'
          }
        }],
        [/(?<!\+)\+(?!\+)|(?<!-)\-(?!-)|[*/%]|(?:==|!=|<=|>=|<|>)/, 'operator'],
        [/[{}]/, 'delimiter.bracket'],
        [/[,:]/, 'delimiter']
      ],
      string: [
        [/[^\\"{]+/, 'string'],
        [/\\./, 'string.escape'],
        [/\{/, { token: 'delimiter.bracket', next: '@interpolation' }],
        [/"/, { token: 'string', next: '@pop' }]
      ],
      interpolation: [
        [/\}/, { token: 'delimiter.bracket', next: '@pop' }],
        [/\d+(\.\d+)?([hms]|ms)?\b/, 'number'],
        [/[a-zA-Z_][a-zA-Z0-9_-]*/, {
          cases: {
            '@operators': 'operator',
            '@types': 'type',
            '@literals': 'literal',
            '@default': 'variable'
          }
        }],
        [/(?<!\+)\+(?!\+)|(?<!-)\-(?!-)|[*/%]|(?:==|!=|<=|>=|<|>)/, 'operator'],
        [/\s+/, 'white']
      ]
    }
  })

  monaco.languages.setLanguageConfiguration('sos', {
    comments: { lineComment: '#' },
    brackets: [['{', '}']],
    autoClosingPairs: [{ open: '{', close: '}' }, { open: '"', close: '"', notIn: ['string'] }],
    indentationRules: {
      increaseIndentPattern: /:\s*$/,
      decreaseIndentPattern: /^\s*(otherwise|on (failure|success|existing))\b/
    }
  })

}
