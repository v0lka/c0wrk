interface UserMessageProps {
  content: string
  timestamp: number
}

export function UserMessage({ content, timestamp }: UserMessageProps) {
  const formattedTime = new Date(timestamp).toLocaleTimeString([], {
    hour: '2-digit',
    minute: '2-digit',
  })

  return (
    <div className="flex flex-col items-end gap-1 max-w-[80%] ml-auto">
      <div className="bg-muted text-foreground rounded-2xl rounded-tr-sm px-4 py-2.5">
        <p className="text-sm whitespace-pre-wrap">{content}</p>
      </div>
      <span className="text-xs text-muted-foreground px-1">{formattedTime}</span>
    </div>
  )
}
