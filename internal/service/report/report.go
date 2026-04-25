package report

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"codescan/internal/model"
)

var ErrNoExportableStages = errors.New("当前没有可导出的已完成审计阶段")

type stageConfig struct {
	Key         string
	Label       string
	Description string
	Accent      string
	Fields      []fieldConfig
}

type fieldConfig struct {
	Key   string
	Label string
	Code  bool
}

type TaskReport struct {
	Task    TaskSummary
	Summary ReportSummary
	Stages  []ReportStage
}

type TaskSummary struct {
	ID          string
	ShortID     string
	Name        string
	Remark      string
	CreatedAt   string
	GeneratedAt string
	FileName    string
}

type ReportSummary struct {
	CompletedStageCount int
	CleanStageCount     int
	TotalFindings       int
	UniqueFiles         int
	UniqueInterfaces    int
	RouteCount          int
	Severities          []SeverityCount
}

type SeverityCount struct {
	Label string
	Count int
	Tone  string
}

type ReportStage struct {
	Key               string
	Label             string
	Description       string
	Accent            string
	FindingCount      int
	RejectedCount     int
	ZeroFindings      bool
	AllRejected       bool
	RawOnly           bool
	RawResult         string
	UniqueFiles       int
	UniqueInterfaces  int
	SummaryText       string
	CompletedAt       string
	SeverityBreakdown []SeverityCount
	Findings          []ReportFinding
	RejectedFindings  []ReportFinding
}

type ReportFinding struct {
	Anchor           string
	Severity         string
	SeverityTone     string
	Verification     string
	ReviewReason     string
	Origin           string
	Subtype          string
	Description      string
	LocationFile     string
	Location         string
	Trigger          string
	TriggerParameter string
	DetailFields     []DisplayField
}

type DisplayField struct {
	Label string
	Value string
	Code  bool
}

var stageConfigs = []stageConfig{
	{
		Key:         "rce",
		Label:       "RCE 审计",
		Description: "远程代码执行深度审计",
		Accent:      "#ef4444",
		Fields: []fieldConfig{
			{Key: "execution_logic", Label: "执行逻辑", Code: true},
			{Key: "vulnerable_code", Label: "漏洞代码", Code: true},
			{Key: "poc", Label: "HTTP POC", Code: true},
			{Key: "impact", Label: "影响", Code: true},
		},
	},
	{
		Key:         "injection",
		Label:       "注入审计",
		Description: "SQL 注入与命令注入深度审计",
		Accent:      "#f59e0b",
		Fields: []fieldConfig{
			{Key: "execution_logic", Label: "执行逻辑", Code: true},
			{Key: "vulnerable_code", Label: "漏洞代码", Code: true},
			{Key: "poc", Label: "HTTP POC", Code: true},
			{Key: "impact", Label: "影响", Code: true},
		},
	},
	{
		Key:         "auth",
		Label:       "认证与会话审计",
		Description: "认证流程与会话管理审计",
		Accent:      "#0ea5e9",
		Fields: []fieldConfig{
			{Key: "auth_mechanism", Label: "认证机制"},
			{Key: "affected_endpoints", Label: "受影响接口", Code: true},
			{Key: "session_artifact", Label: "会话凭据", Code: true},
			{Key: "execution_logic", Label: "执行逻辑", Code: true},
			{Key: "vulnerable_code", Label: "漏洞代码", Code: true},
			{Key: "poc_http", Label: "原始 HTTP POC", Code: true},
			{Key: "trigger_steps", Label: "触发步骤", Code: true},
			{Key: "impact", Label: "影响", Code: true},
		},
	},
	{
		Key:         "access",
		Label:       "访问控制审计",
		Description: "授权与访问边界审计",
		Accent:      "#6366f1",
		Fields: []fieldConfig{
			{Key: "authentication_state", Label: "认证状态"},
			{Key: "required_privilege", Label: "所需权限"},
			{Key: "access_boundary", Label: "访问边界"},
			{Key: "attacker_profile", Label: "攻击者画像"},
			{Key: "target_profile", Label: "目标画像"},
			{Key: "target_resource", Label: "目标资源"},
			{Key: "affected_endpoints", Label: "受影响接口", Code: true},
			{Key: "authorization_logic", Label: "授权逻辑", Code: true},
			{Key: "bypass_vector", Label: "绕过方式", Code: true},
			{Key: "execution_logic", Label: "执行逻辑", Code: true},
			{Key: "vulnerable_code", Label: "漏洞代码", Code: true},
			{Key: "poc_http", Label: "原始 HTTP POC", Code: true},
			{Key: "trigger_steps", Label: "触发步骤", Code: true},
			{Key: "impact", Label: "影响", Code: true},
		},
	},
	{
		Key:         "xss",
		Label:       "XSS 审计",
		Description: "反射型、存储型与 DOM XSS 审计",
		Accent:      "#10b981",
		Fields: []fieldConfig{
			{Key: "sink_type", Label: "危险汇点类型"},
			{Key: "render_context", Label: "渲染上下文"},
			{Key: "storage_point", Label: "存储点"},
			{Key: "payload_hint", Label: "Payload 提示", Code: true},
			{Key: "execution_logic", Label: "执行逻辑", Code: true},
			{Key: "vulnerable_code", Label: "漏洞代码", Code: true},
			{Key: "poc_http", Label: "原始 HTTP POC", Code: true},
			{Key: "trigger_steps", Label: "触发步骤", Code: true},
			{Key: "expected_execution", Label: "预期执行效果", Code: true},
			{Key: "impact", Label: "影响", Code: true},
		},
	},
	{
		Key:         "config",
		Label:       "配置与组件审计",
		Description: "配置暴露与依赖组件风险审计",
		Accent:      "#06b6d4",
		Fields: []fieldConfig{
			{Key: "proof_type", Label: "证明类型"},
			{Key: "configuration_item", Label: "配置项"},
			{Key: "reference_id", Label: "参考编号"},
			{Key: "affected_endpoints", Label: "受影响接口", Code: true},
			{Key: "exposure_mechanism", Label: "暴露机制", Code: true},
			{Key: "upgrade_recommendation", Label: "升级建议", Code: true},
			{Key: "execution_logic", Label: "执行逻辑", Code: true},
			{Key: "vulnerable_code", Label: "漏洞代码 / 配置", Code: true},
			{Key: "poc_http", Label: "原始 HTTP POC", Code: true},
			{Key: "reproduction_steps", Label: "复现步骤", Code: true},
			{Key: "impact", Label: "影响", Code: true},
		},
	},
	{
		Key:         "fileop",
		Label:       "文件操作审计",
		Description: "上传、下载、遍历与包含类风险审计",
		Accent:      "#f97316",
		Fields: []fieldConfig{
			{Key: "file_operation", Label: "文件操作类型"},
			{Key: "input_vector", Label: "输入向量"},
			{Key: "target_path", Label: "目标路径"},
			{Key: "validation_logic", Label: "校验逻辑", Code: true},
			{Key: "payload_hint", Label: "Payload 提示", Code: true},
			{Key: "execution_logic", Label: "执行逻辑", Code: true},
			{Key: "vulnerable_code", Label: "漏洞代码", Code: true},
			{Key: "poc_http", Label: "原始 HTTP POC", Code: true},
			{Key: "trigger_steps", Label: "触发步骤", Code: true},
			{Key: "impact", Label: "影响", Code: true},
		},
	},
	{
		Key:         "logic",
		Label:       "业务逻辑审计",
		Description: "流程滥用与业务规则绕过审计",
		Accent:      "#f43f5e",
		Fields: []fieldConfig{
			{Key: "proof_type", Label: "证明类型"},
			{Key: "workflow_name", Label: "业务流程名称"},
			{Key: "business_action", Label: "业务动作"},
			{Key: "affected_endpoints", Label: "受影响接口", Code: true},
			{Key: "manipulated_fields", Label: "可操纵字段", Code: true},
			{Key: "preconditions", Label: "前置条件", Code: true},
			{Key: "state_transition", Label: "状态迁移", Code: true},
			{Key: "race_window", Label: "竞态窗口", Code: true},
			{Key: "bypass_vector", Label: "绕过方式", Code: true},
			{Key: "execution_logic", Label: "执行逻辑", Code: true},
			{Key: "vulnerable_code", Label: "漏洞代码", Code: true},
			{Key: "poc_http", Label: "原始 HTTP POC", Code: true},
			{Key: "trigger_steps", Label: "触发步骤", Code: true},
			{Key: "impact", Label: "影响", Code: true},
		},
	},
}

func GenerateHTML(task model.Task, generatedAt time.Time) ([]byte, string, error) {
	report, err := Build(task, generatedAt)
	if err != nil {
		return nil, "", err
	}

	var buf bytes.Buffer
	if err := taskReportTemplate.Execute(&buf, report); err != nil {
		return nil, "", fmt.Errorf("render report template: %w", err)
	}

	return buf.Bytes(), report.Task.FileName, nil
}
