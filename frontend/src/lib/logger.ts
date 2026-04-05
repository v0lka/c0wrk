type LogLevel = 'debug' | 'info' | 'warn' | 'error' | 'silent'

const levelOrder: Record<LogLevel, number> = {
  debug: 0, info: 1, warn: 2, error: 3, silent: 4,
}

let currentLevel: LogLevel = 'warn'

export function setLogLevel(level: LogLevel): void {
  currentLevel = level
}

export const logger = {
  debug: (...args: unknown[]) => { if (levelOrder[currentLevel] <= 0) console.debug(...args) },
  info:  (...args: unknown[]) => { if (levelOrder[currentLevel] <= 1) console.info(...args) },
  warn:  (...args: unknown[]) => { if (levelOrder[currentLevel] <= 2) console.warn(...args) },
  error: (...args: unknown[]) => { if (levelOrder[currentLevel] <= 3) console.error(...args) },
}
