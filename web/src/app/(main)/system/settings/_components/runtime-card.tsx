"use client";

import * as React from "react";
import { CheckCircle2Icon, RefreshCwIcon, TriangleAlertIcon } from "lucide-react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { api } from "@/lib/api";
import type { RuntimeInfo } from "@/lib/types";
import { pausedFeatures, toolStatusLabel } from "@/lib/runtime-view";

const BACKGROUND_FEATURES = [
  ["scheduler", "定时调度", "恢复已保存的定时调度配置"],
  ["notifier", "通知投递", "将处理保留的待投递事件和通知"],
  ["mcp", "MCP 发现", "可能启动已保存的 MCP 服务进程"],
  ["archives", "归档后台处理", "恢复排队归档任务的处理"],
] as const;

export function RuntimeCard() {
  const [info, setInfo] = React.useState<RuntimeInfo | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState("");
  const load = React.useCallback((rescan = false) => {
    setBusy(true);
    setError("");
    (rescan ? api.rescanRuntime() : api.runtime())
      .then(setInfo)
      .catch((error) => setError(`读取运行环境失败：${(error as Error).message}`))
      .finally(() => setBusy(false));
  }, []);
  React.useEffect(() => load(), [load]);
  const resume = (feature: string) => {
    if (!info) return;
    setBusy(true);
    api.resumeRuntime([feature]).then(setInfo).catch((error) => toast.error(`恢复后台功能失败：${(error as Error).message}`)).finally(() => setBusy(false));
  };
  return <Card className="mb-4 break-inside-avoid md:mb-6">
    <CardHeader>
      <CardTitle className="flex items-center gap-2 text-base"><CheckCircle2Icon className="size-4" />运行环境 · VulnHarvest v0.0.1</CardTitle>
      <CardDescription>只检测当前会话可见的本地工具。缺少可选工具只影响对应能力，不阻塞应用。</CardDescription>
    </CardHeader>
    <CardContent className="space-y-4">
      {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
      {info ? <>
        <div className="flex flex-wrap gap-2 text-sm"><Badge variant="secondary">模式 · {info.mode}</Badge><Badge variant="outline">HTTP · {info.http_address}</Badge><Badge variant="outline">代理 · {info.proxy_address || "未启用"}</Badge><Badge variant="outline">PTY · {info.environment.pty_status}</Badge></div>
        <div className="grid gap-2 sm:grid-cols-2">
          {Object.values(info.environment.tools).map((tool) => <div key={tool.name} className="rounded-md border p-2 text-xs">
            <div className="flex items-center justify-between gap-2"><b>{tool.name}</b><Badge variant={tool.status === "ready" ? "default" : "outline"}>{toolStatusLabel(tool.status)}</Badge></div>
            <div className="mt-1 text-muted-foreground">来源：{tool.source || "未检测"}{tool.version ? ` · ${tool.version}` : ""}</div>
            {tool.path && <div className="truncate font-mono text-muted-foreground" title={tool.path}>{tool.path}</div>}
            {tool.affected_features.length > 0 && <div className="mt-1 text-amber-700 dark:text-amber-400">影响：{tool.affected_features.join("、")}</div>}
            {tool.candidate_failures?.map((failure, index) => <p key={`${failure.source}-${index}`} className="mt-1 break-words text-muted-foreground">回退候选 · {failure.source} · {failure.path}：{failure.reason}</p>)}
          </div>)}
        </div>
        <div className="space-y-3 rounded-md border p-3">
          <p className="text-sm font-medium">本会话后台功能</p>
          {BACKGROUND_FEATURES.map(([feature, label, effect]) => <div key={feature} className="flex items-start justify-between gap-3 text-xs">
            <div><b>{label} · {info.background[feature] ? "已启用" : "暂停"}</b><p className="mt-1 text-muted-foreground">{effect}</p></div>
            {pausedFeatures(info.background).includes(feature) && <Button variant="outline" size="sm" onClick={() => resume(feature)} disabled={busy}>恢复本会话</Button>}
          </div>)}
        </div>
        <p className="text-xs text-muted-foreground">{info.pending_delivery_effect}</p>
      </> : <p className="text-sm text-muted-foreground"><TriangleAlertIcon className="mr-1 inline size-4" />{busy ? "正在读取运行环境…" : "运行环境暂不可用"}</p>}
      <Button variant="outline" size="sm" onClick={() => load(true)} disabled={busy}><RefreshCwIcon className={busy ? "size-4 animate-spin" : "size-4"} />重新检测</Button>
    </CardContent>
  </Card>;
}
