import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

export function UpdateCard() {
  return <Card className="mb-4 break-inside-avoid md:mb-6">
    <CardHeader><CardTitle className="text-base">VulnHarvest v0.0.1</CardTitle></CardHeader>
    <CardContent className="text-sm text-muted-foreground">已停用上游 ARTEX 自动更新和换装。升级时请关闭应用并完整备份目录，再手动安装经审核的 VulnHarvest 包。</CardContent>
  </Card>;
}
