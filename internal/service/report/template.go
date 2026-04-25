package report

import (
	"html/template"
	"strings"
)

var taskReportTemplate = template.Must(template.New("task_report").Funcs(template.FuncMap{
	"hasRemark": func(remark string) bool {
		return strings.TrimSpace(remark) != ""
	},
	"hasString": func(value string) bool {
		return strings.TrimSpace(value) != ""
	},
	"severityChipStyle": func(tone string) string {
		switch tone {
		case "critical":
			return "background:#fef2f2;color:#b91c1c;border:1px solid #fecaca;"
		case "high":
			return "background:#fff7ed;color:#c2410c;border:1px solid #fdba74;"
		case "medium":
			return "background:#fffbeb;color:#a16207;border:1px solid #fde68a;"
		case "low":
			return "background:#eff6ff;color:#1d4ed8;border:1px solid #bfdbfe;"
		default:
			return "background:#f8fafc;color:#475569;border:1px solid #cbd5e1;"
		}
	},
	"severityLabel": func(label string) string {
		switch strings.ToUpper(strings.TrimSpace(label)) {
		case "CRITICAL":
			return "严重"
		case "HIGH":
			return "高危"
		case "MEDIUM":
			return "中危"
		case "LOW":
			return "低危"
		case "INFO":
			return "提示"
		case "UNKNOWN":
			return "未知"
		default:
			return strings.TrimSpace(label)
		}
	},
	"verificationChipStyle": func(status string) string {
		switch strings.ToLower(strings.TrimSpace(status)) {
		case "confirmed":
			return "background:#ecfdf5;color:#047857;border:1px solid #a7f3d0;"
		case "uncertain":
			return "background:#fffbeb;color:#b45309;border:1px solid #fcd34d;"
		case "rejected":
			return "background:#fff1f2;color:#be123c;border:1px solid #fda4af;"
		default:
			return "background:#f8fafc;color:#475569;border:1px solid #cbd5e1;"
		}
	},
	"verificationLabel": func(status string) string {
		switch strings.ToLower(strings.TrimSpace(status)) {
		case "confirmed":
			return "已确认"
		case "uncertain":
			return "待确认"
		case "rejected":
			return "已驳回"
		case "unreviewed":
			return "未复核"
		default:
			return strings.TrimSpace(status)
		}
	},
	"originLabel": func(origin string) string {
		switch strings.ToLower(strings.TrimSpace(origin)) {
		case "initial":
			return "初始结果"
		case "gap_check":
			return "补扫结果"
		default:
			return strings.TrimSpace(origin)
		}
	},
}).Parse(reportHTMLTemplate))

const reportHTMLTemplate = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>{{ .Task.Name }} - CodeScan 审计报告</title>
  <style>
    :root {
      color-scheme: light;
      --bg: #f4f7fb;
      --paper: #ffffff;
      --ink: #0f172a;
      --muted: #475569;
      --line: #dbe4f0;
      --soft: #eef4fb;
      --shadow: 0 18px 45px rgba(15, 23, 42, 0.08);
      --radius: 22px;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      font-family: "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif;
      background: radial-gradient(circle at top left, rgba(59,130,246,0.10), transparent 32%), linear-gradient(180deg, #fbfdff 0%, var(--bg) 100%);
      color: var(--ink);
      line-height: 1.6;
    }
    .page { max-width: 1240px; margin: 0 auto; padding: 32px; }
    .hero, .summary-card, .stage-shell, .finding-card, .meta-card { background: var(--paper); border: 1px solid var(--line); border-radius: var(--radius); box-shadow: var(--shadow); }
    .hero {
      position: relative;
      overflow: hidden;
      background: linear-gradient(135deg, #ffffff 0%, #f6f9ff 62%, #edf4ff 100%);
      padding: 32px;
      margin-bottom: 24px;
    }
    .hero::after {
      content: "";
      position: absolute;
      inset: auto -80px -100px auto;
      width: 240px;
      height: 240px;
      background: radial-gradient(circle, rgba(59,130,246,0.18), transparent 70%);
    }
    .eyebrow, .meta-label, .section-eyebrow, .field-label, .stat-label {
      font-size: 12px;
      font-weight: 700;
      letter-spacing: 0.1em;
      text-transform: uppercase;
    }
    .eyebrow {
      display: inline-flex;
      align-items: center;
      gap: 8px;
      color: #2563eb;
      background: rgba(37,99,235,0.08);
      padding: 8px 12px;
      border-radius: 999px;
      border: 1px solid rgba(37,99,235,0.14);
    }
    h1 { margin: 18px 0 10px; font-size: 36px; line-height: 1.15; }
    .hero-grid { display: grid; grid-template-columns: 1.45fr 0.9fr; gap: 24px; align-items: start; }
    .meta-grid, .summary-grid, .stage-summary, .finding-grid { display: grid; gap: 14px; }
    .meta-grid, .summary-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
    .meta-card, .summary-card { padding: 18px 20px; }
    .meta-label, .section-eyebrow, .field-label, .stat-label { color: #64748b; margin-bottom: 6px; }
    .meta-value, .stat-value { color: var(--ink); font-weight: 600; word-break: break-word; }
    .stat-value strong { font-size: 28px; color: #0f172a; }
    .pill-row, .severity-row { display: flex; flex-wrap: wrap; gap: 10px; }
    .pill-row { margin-top: 18px; }
    .stage-pill, .severity-chip {
      display: inline-flex;
      align-items: center;
      gap: 8px;
      padding: 7px 11px;
      border-radius: 999px;
      font-size: 12px;
      font-weight: 700;
    }
    .stage-pill {
      color: var(--ink);
      border: 1px solid color-mix(in srgb, var(--accent) 22%, white);
      background: color-mix(in srgb, var(--accent) 8%, white);
    }
    section { margin-top: 28px; }
    .section-title { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 14px; }
    .section-title h2 { margin: 0; font-size: 24px; }
    .stage-shell { overflow: hidden; margin-bottom: 20px; border-left: 8px solid var(--accent); }
    .stage-head {
      padding: 22px 24px 18px;
      background: linear-gradient(140deg, color-mix(in srgb, var(--accent) 10%, white), white 58%);
      border-bottom: 1px solid var(--line);
    }
    .stage-head-top { display: flex; justify-content: space-between; align-items: start; gap: 14px; }
    .stage-title { margin: 0; font-size: 22px; }
    .stage-description, .finding-desc { color: var(--muted); font-size: 14px; }
    .stage-description { margin: 8px 0 0; }
    .finding-desc { margin: 0; }
    .stage-summary { margin-top: 16px; grid-template-columns: repeat(4, minmax(0, 1fr)); }
    .stage-stat {
      padding: 14px;
      border-radius: 16px;
      background: rgba(255,255,255,0.70);
      border: 1px solid rgba(148,163,184,0.18);
    }
    .stage-stat strong { display: block; font-size: 22px; margin-top: 4px; color: var(--ink); }
    .stage-body { padding: 22px 24px 26px; }
    .state-note {
      padding: 18px 20px;
      border-radius: 18px;
      border: 1px dashed #94a3b8;
      background: #f8fbff;
      color: #334155;
    }
    .finding-card { padding: 18px 18px 20px; margin-top: 14px; }
    .finding-header { display: flex; justify-content: space-between; align-items: start; gap: 12px; }
    .finding-title { margin: 8px 0 6px; font-size: 19px; }
    .finding-grid { margin-top: 16px; grid-template-columns: repeat(2, minmax(0, 1fr)); }
    .field-card { background: var(--soft); border: 1px solid var(--line); border-radius: 16px; padding: 14px; }
    .field-card pre {
      margin: 0;
      white-space: pre-wrap;
      word-break: break-word;
      font-size: 13px;
      line-height: 1.6;
      color: #0f172a;
      font-family: "Cascadia Code", "Consolas", monospace;
    }
    .field-value { font-size: 14px; color: #0f172a; word-break: break-word; }
    .footer { margin-top: 28px; color: #64748b; text-align: center; font-size: 12px; }
    @media (max-width: 980px) {
      .page { padding: 20px; }
      .hero-grid, .summary-grid, .meta-grid, .stage-summary, .finding-grid { grid-template-columns: 1fr; }
    }
    @media print {
      body { background: white; }
      .page { max-width: none; padding: 0; }
      .hero, .summary-card, .stage-shell, .finding-card, .meta-card { box-shadow: none; }
      section, .stage-shell, .finding-card { break-inside: avoid; }
    }
  </style>
</head>
<body>
  <main class="page">
    <section class="hero">
      <div class="eyebrow">CodeScan 审计报告</div>
      <div class="hero-grid">
        <div>
          <h1>{{ .Task.Name }}</h1>
          <p style="margin:0;color:#475569;font-size:15px;">任务级导出报告，自动汇总当前任务中已完成的审计阶段，并整理关键风险、影响范围与证据片段。</p>
          <div class="pill-row">
            {{ range .Stages }}
              <span class="stage-pill" style="--accent: {{ .Accent }};">{{ .Label }}</span>
            {{ end }}
          </div>
        </div>
        <div class="meta-grid">
          <div class="meta-card">
            <div class="meta-label">任务 ID</div>
            <div class="meta-value">{{ .Task.ID }}</div>
          </div>
          <div class="meta-card">
            <div class="meta-label">创建时间</div>
            <div class="meta-value">{{ .Task.CreatedAt }}</div>
          </div>
          <div class="meta-card">
            <div class="meta-label">导出时间</div>
            <div class="meta-value">{{ .Task.GeneratedAt }}</div>
          </div>
          <div class="meta-card">
            <div class="meta-label">纳入阶段</div>
            <div class="meta-value">{{ .Summary.CompletedStageCount }}</div>
          </div>
          {{ if hasRemark .Task.Remark }}
          <div class="meta-card" style="grid-column: 1 / -1;">
            <div class="meta-label">备注</div>
            <div class="meta-value">{{ .Task.Remark }}</div>
          </div>
          {{ end }}
        </div>
      </div>
    </section>

    <section>
      <div class="section-title">
        <div>
          <div class="section-eyebrow">概要</div>
          <h2>整体概览</h2>
        </div>
        <div class="severity-row">
          {{ range .Summary.Severities }}
            <span class="severity-chip" style="{{ severityChipStyle .Tone }}">{{ severityLabel .Label }} · {{ .Count }}</span>
          {{ end }}
        </div>
      </div>
      <div class="summary-grid">
        <div class="summary-card">
          <div class="stat-label">已完成审计</div>
          <div class="stat-value"><strong>{{ .Summary.CompletedStageCount }}</strong> 个阶段</div>
        </div>
        <div class="summary-card">
          <div class="stat-label">已确认发现</div>
          <div class="stat-value"><strong>{{ .Summary.TotalFindings }}</strong> 条</div>
        </div>
        <div class="summary-card">
          <div class="stat-label">唯一源码位置</div>
          <div class="stat-value"><strong>{{ .Summary.UniqueFiles }}</strong> 个文件</div>
        </div>
        <div class="summary-card">
          <div class="stat-label">接口覆盖面</div>
          <div class="stat-value"><strong>{{ .Summary.UniqueInterfaces }}</strong> 个路由或端点</div>
        </div>
        <div class="summary-card">
          <div class="stat-label">路由清单</div>
          <div class="stat-value"><strong>{{ .Summary.RouteCount }}</strong> 条已扫描路由</div>
        </div>
        <div class="summary-card">
          <div class="stat-label">零发现阶段</div>
          <div class="stat-value"><strong>{{ .Summary.CleanStageCount }}</strong> 个阶段</div>
        </div>
      </div>
    </section>

    <section>
      <div class="section-title">
        <div>
          <div class="section-eyebrow">模块</div>
          <h2>已纳入导出的审计阶段</h2>
        </div>
      </div>

      {{ range .Stages }}
      <article class="stage-shell" style="--accent: {{ .Accent }};">
        <div class="stage-head">
          <div class="stage-head-top">
            <div>
              <h3 class="stage-title">{{ .Label }}</h3>
              <p class="stage-description">{{ .Description }}</p>
            </div>
            <div class="severity-row">
              {{ range .SeverityBreakdown }}
                <span class="severity-chip" style="{{ severityChipStyle .Tone }}">{{ severityLabel .Label }} · {{ .Count }}</span>
              {{ end }}
            </div>
          </div>
          <div class="stage-summary">
            <div class="stage-stat">
              <div class="stat-label">发现数</div>
              <strong>{{ .FindingCount }}</strong>
            </div>
            <div class="stage-stat">
              <div class="stat-label">文件数</div>
              <strong>{{ .UniqueFiles }}</strong>
            </div>
            <div class="stage-stat">
              <div class="stat-label">接口数</div>
              <strong>{{ .UniqueInterfaces }}</strong>
            </div>
            <div class="stage-stat">
              <div class="stat-label">驳回数</div>
              <strong>{{ .RejectedCount }}</strong>
            </div>
            <div class="stage-stat">
              <div class="stat-label">完成时间</div>
              <strong style="font-size:15px;">{{ .CompletedAt }}</strong>
            </div>
          </div>
        </div>

        <div class="stage-body">
          <p style="margin-top:0;color:#334155;">{{ .SummaryText }}</p>

          {{ if .RawOnly }}
            <div class="state-note">
              <div class="field-card">
                <div class="field-label">原始 AI 输出</div>
                <pre>{{ .RawResult }}</pre>
              </div>
            </div>
          {{ else if .AllRejected }}
            <div class="state-note">{{ .SummaryText }}</div>
          {{ else if .ZeroFindings }}
            <div class="state-note">该阶段已执行完成，未发现已确认漏洞。本结果会保留在导出报告中，便于后续追踪与留档。</div>
          {{ else }}
            {{ range .Findings }}
              <article class="finding-card" id="{{ .Anchor }}">
                <div class="finding-header">
                  <div>
                    <div class="severity-row">
                      <span class="severity-chip" style="{{ severityChipStyle .SeverityTone }}">{{ severityLabel .Severity }}</span>
                      <span class="severity-chip" style="{{ verificationChipStyle .Verification }}">{{ verificationLabel .Verification }}</span>
                      {{ if hasString .Origin }}
                        <span class="severity-chip" style="background:#eff6ff;color:#1d4ed8;border:1px solid #bfdbfe;">{{ originLabel .Origin }}</span>
                      {{ end }}
                    </div>
                    <h4 class="finding-title">{{ .Subtype }}</h4>
                    <p class="finding-desc">{{ .Description }}</p>
                    {{ if hasString .ReviewReason }}
                      <p class="finding-desc" style="margin-top:8px;"><strong>复核说明：</strong>{{ .ReviewReason }}</p>
                    {{ end }}
                  </div>
                  {{ if hasString .Location }}
                    <div style="text-align:right;color:#475569;font-size:13px;">{{ .Location }}</div>
                  {{ end }}
                </div>

                <div class="finding-grid">
                  {{ if hasString .Trigger }}
                    <div class="field-card">
                      <div class="field-label">触发接口</div>
                      <div class="field-value">{{ .Trigger }}</div>
                      {{ if hasString .TriggerParameter }}
                        <div class="field-value" style="margin-top:8px;color:#475569;">参数：{{ .TriggerParameter }}</div>
                      {{ end }}
                    </div>
                  {{ end }}

                  {{ if hasString .Location }}
                    <div class="field-card">
                      <div class="field-label">源码位置</div>
                      <div class="field-value">{{ .Location }}</div>
                    </div>
                  {{ end }}

                  {{ range .DetailFields }}
                    <div class="field-card">
                      <div class="field-label">{{ .Label }}</div>
                      {{ if .Code }}
                        <pre>{{ .Value }}</pre>
                      {{ else }}
                        <div class="field-value">{{ .Value }}</div>
                      {{ end }}
                    </div>
                  {{ end }}
                </div>
              </article>
            {{ end }}
            {{ if gt .RejectedCount 0 }}
              <div class="state-note" style="margin-top:16px;">
                <div class="field-label">已驳回发现附录</div>
                {{ range .RejectedFindings }}
                  <article class="finding-card" id="{{ .Anchor }}-rejected" style="box-shadow:none;margin-top:14px;">
                    <div class="finding-header">
                      <div>
                        <div class="severity-row">
                          <span class="severity-chip" style="{{ severityChipStyle .SeverityTone }}">{{ severityLabel .Severity }}</span>
                          <span class="severity-chip" style="{{ verificationChipStyle .Verification }}">{{ verificationLabel .Verification }}</span>
                          {{ if hasString .Origin }}
                            <span class="severity-chip" style="background:#eff6ff;color:#1d4ed8;border:1px solid #bfdbfe;">{{ originLabel .Origin }}</span>
                          {{ end }}
                        </div>
                        <h4 class="finding-title">{{ .Subtype }}</h4>
                        <p class="finding-desc">{{ .Description }}</p>
                        {{ if hasString .ReviewReason }}
                          <p class="finding-desc" style="margin-top:8px;"><strong>复核说明：</strong>{{ .ReviewReason }}</p>
                        {{ end }}
                      </div>
                      {{ if hasString .Location }}
                        <div style="text-align:right;color:#475569;font-size:13px;">{{ .Location }}</div>
                      {{ end }}
                    </div>
                  </article>
                {{ end }}
              </div>
            {{ end }}
          {{ end }}
        </div>
      </article>
      {{ end }}
    </section>

    <div class="footer">
      由 CodeScan 生成。本 HTML 文件为自包含格式，可离线打开。
    </div>
  </main>
</body>
</html>
`
