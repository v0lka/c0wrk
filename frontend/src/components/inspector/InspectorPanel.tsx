import { PanelRight } from 'lucide-react'
import { useInspectorStore } from '@/stores/inspectorStore'
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs'
import { ScrollArea, ScrollBar } from '@/components/ui/scroll-area'
import { GlobalView } from './MemoryView'
import { SessionView } from './SessionView'

export function InspectorPanel() {
  const { activeTab, setTab } = useInspectorStore()

  return (
    <aside className="h-full bg-card flex flex-col">
      {/* Header */}
      <div className="flex items-center px-4 py-3 border-b border-border">
        <div className="flex items-center gap-2">
          <PanelRight className="h-4 w-4 text-muted-foreground" />
          <h3 className="text-sm font-semibold">Inspector</h3>
        </div>
      </div>

      {/* Tabs */}
      <Tabs
        value={activeTab}
        onValueChange={(value) => setTab(value as 'session' | 'global')}
        className="flex-1 flex flex-col min-h-0"
      >
        <TabsList variant="line" className="w-full justify-start px-4 py-2 border-b border-border rounded-none bg-transparent">
          <TabsTrigger value="session" className="text-xs">Workspace</TabsTrigger>
          <TabsTrigger value="global" className="text-xs">Memory</TabsTrigger>
        </TabsList>

        <ScrollArea className="flex-1" type="auto">
          <div className="w-full min-w-0 overflow-hidden">
            <TabsContent value="session" className="m-0 p-4 min-w-0">
              <SessionView />
            </TabsContent>
            <TabsContent value="global" className="m-0 p-4 min-w-0">
              <GlobalView />
            </TabsContent>
          </div>
          <ScrollBar orientation="horizontal" />
        </ScrollArea>
      </Tabs>
    </aside>
  )
}
