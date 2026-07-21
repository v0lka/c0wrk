import { useCallback } from 'react'
import { Sun, Moon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useThemeStore, type Theme } from '@/stores/themeStore'

interface ThemeOption {
  value: Theme
  label: string
}

const themeOptions: ThemeOption[] = [
  { value: 'dark', label: 'Dark' },
  { value: 'light', label: 'Light' },
]

export function ThemeSelector() {
  const theme = useThemeStore((s) => s.theme)
  const setTheme = useThemeStore((s) => s.setTheme)

  const handleThemeChange = useCallback((next: Theme) => {
    setTheme(next)
  }, [setTheme])

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
            {option.value === 'light' ? (
              <Sun className="size-3.5" />
            ) : (
              <Moon className="size-3.5" />
            )}
            <span className="text-xs">{option.label}</span>
          </Button>
        ))}
      </div>
    </div>
  )
}
