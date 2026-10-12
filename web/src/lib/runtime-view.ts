export type RuntimeToolState = "ready" | "missing" | "unverified" | "incompatible" | string;

export function toolStatusLabel(status: RuntimeToolState): string {
  const labels: Record<string, string> = { ready: "可用", missing: "缺失", unverified: "未验证", incompatible: "不兼容" };
  return labels[status] ?? status;
}

export function pausedFeatures(background: Record<string, boolean>): string[] {
  return ["scheduler", "notifier", "mcp", "archives"].filter((feature) => !background[feature]);
}
