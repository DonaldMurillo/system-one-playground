// Immediate offline lexical colors; semantic meaning comes from the shared SOS LSP.

export const SENTENCE_STARTERS = [
  'evaluate', 'criterion', 'package', 'import', 'export', 'argument', 'option', 'switch', 'to', 'call', 'return', 'while', 'repeat', 'set', 'emit', 'judge', 'score', 'command', 'describe', 'expect', 'remember', 'find', 'read', 'require',
  'keep', 'sort', 'group', 'create', 'make', 'assign', 'when', 'take',
  'classify', 'append', 'save', 'show', 'for', 'map', 'stop', 'print'
]

export const CONNECTORS = [
  'each', 'in', 'as', 'into', 'on', 'called', 'with', 'where', 'by', 'from',
  'under', 'named', 'matching', 'if', 'missing', 'numbered', 'otherwise',
  'and', 'or', 'not', 'of', 'count', 'first', 'last', 'items', 'default',
  'choices', 'is', 'ascending', 'descending', 'existing', 'off', 'at', 'most', 'running', 'collecting', 'failures', 'rethrow'
]


export const TYPE_WORDS = [
  'text', 'timestamp', 'duration', 'folder', 'list', 'json', 'table',
  'number', 'integer', 'boolean', 'switch'
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
    types: TYPE_WORDS,
    judgment: JUDGMENT_WORDS,
    literals: LITERALS,
    tokenizer: {
      root: [
        [/#.*$/, 'comment'],
        [/"([^"\\]|\\.)*"?/, 'string'],
        [/\d+(\.\d+)?([hms]|ms)?\b/, 'number'],
        [/[a-zA-Z_][a-zA-Z0-9_-]*/, {
          cases: {
            '@starters': 'keyword',
            '@connectors': 'connector',
            '@judgment': 'judgment',
            '@types': 'type',
            '@literals': 'literal',
            '@default': 'identifier'
          }
        }],
        [/[{}]/, 'delimiter.bracket'],
        [/[,:]/, 'delimiter']
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
