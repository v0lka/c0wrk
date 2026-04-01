import { useEffect } from 'react'
import { Sun, Moon, Monitor } from 'lucide-react'
import { useUIStore } from '@/stores/uiStore'
import { Button } from '@/components/ui/button'
import { SetTheme, GetConfig } from '../../../wailsjs/go/main/App'
import type { ReactNode } from 'react'

type Theme = 'light' | 'dark' | 'system'

interface ThemeOption {
  value: Theme
  label: string
  icon: ReactNode
}

const themeOptions: ThemeOption[] = [
  { value: 'light', label: 'Light', icon: <Sun className="h-4 w-4" /> },
  { value: 'dark', label: 'Dark', icon: <Moon className="h-4 w-4" /> },
  { value: 'system', label: 'System', icon: <Monitor className="h-4 w-4" /> },
]

export function ThemeToggle() {
  const theme = useUIStore((s) => s.theme)
  const setThemeStore = useUIStore((s) => s.setTheme)

  useEffect(() => {
    GetConfig()
      .then((config: Record<string, unknown>) => {
        const savedTheme = config?.theme as string
        if (savedTheme && ['light', 'dark', 'system'].includes(savedTheme)) {
          setThemeStore(savedTheme as Theme)
        }
      })
      .catch(() => {
        // Keep default if fetch fails
      })
  }, [setThemeStore])

  const handleThemeChange = async (newTheme: Theme) => {
    setThemeStore(newTheme)
    try {
      await SetTheme(newTheme)
    } catch {
      // Theme is already applied locally, persistence failure is non-fatal
    }
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center justify-between">
        <span className="text-sm font-medium">Theme</span>
      </div>
      <div className="flex gap-1 p-1 bg-muted rounded-lg">
        {themeOptions.map((option) => (
          <Button
            key={option.value}
            variant={theme === option.value ? 'secondary' : 'ghost'}
            size="sm"
            className={`flex-1 gap-2 justify-center transition-all duration-200 ${
              theme === option.value
                ? 'bg-background shadow-sm text-foreground'
                : 'text-muted-foreground hover:text-foreground'
            }`}
            onClick={() => handleThemeChange(option.value)}
          >
            {option.icon}
            <span className="text-xs">{option.label}</span>
          </Button>
        ))}
      </div>
    </div>
  )
}
